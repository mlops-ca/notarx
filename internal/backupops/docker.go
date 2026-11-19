package backupops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/objectstore"
)

// fsObjectMount is where the restored filesystem-driver Gateway sees its
// object root. The host directory behind it is a new private directory owned
// by the invoking user, and the Gateway runs as that user.
const fsObjectMount = "/srv/notarx/objects"

// Pinned by digest like rustfsImage: the owned-resource checks assert the
// image's declared volume layout (/var/lib/postgresql since Postgres 18).
const postgresImage = "postgres:18-alpine@sha256:b6972e1115a4ff8eba5d7f7d19184926d1e16d7d1136ae1e1a7ebf286da0f015"
const rustfsImage = "rustfs/rustfs:1.0.0-beta.12@sha256:41fe89380f4120a337790c02af192c3fe7bb55c3edc2e6e9357b487b47c6ab21"

var errCleanupIncomplete = errors.New("owned resource cleanup incomplete")

var resourceName = regexp.MustCompile(`^[a-z][a-z0-9-]{5,62}$`)

// Docker selects an explicit local unix-socket context. Environment variables
// cannot redirect recovery to a remote daemon or another Compose project.
type Docker struct {
	Context string `json:"context"`
}

func (d Docker) command(ctx context.Context, extra []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--context", d.Context}, args...)...)
	cmd.Env = sanitizedEnvironment(extra)
	cmd.Stderr = io.Discard
	return cmd
}
func (d Docker) output(ctx context.Context, args ...string) ([]byte, error) {
	data, err := d.command(ctx, nil, args...).Output()
	if err != nil {
		return nil, errors.New("local Docker operation failed")
	}
	if len(data) > 8<<20 {
		return nil, errors.New("Docker response too large")
	}
	return data, nil
}
func (d Docker) Validate(ctx context.Context) error {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(d.Context) {
		return errors.New("explicit local Docker context required")
	}
	data, err := d.output(ctx, "context", "inspect", d.Context)
	if err != nil {
		return err
	}
	var entries []struct {
		Endpoints map[string]struct{ Host string }
	}
	if json.Unmarshal(data, &entries) != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Endpoints["docker"].Host, "unix:///") {
		return errors.New("remote Docker recovery unsupported")
	}
	return nil
}
func (d Docker) ObserveRelease(ctx context.Context, release Release) (Release, error) {
	g := release.Identity
	if g.Artifact != nil && g.Artifact.Kind == "binary" {
		return release, nil
	}
	expected := g.ImageDigest
	if g.Artifact != nil {
		expected = g.Artifact.SHA256
	}
	if expected == "" || !strings.HasSuffix(release.ImageReference, "@"+expected) {
		return release, errRelease
	}
	data, err := d.output(ctx, "image", "inspect", release.ImageReference)
	if err != nil {
		return release, errRelease
	}
	var images []struct {
		ID           string `json:"Id"`
		RepoDigests  []string
		Architecture string
		Os           string
		Config       struct{ Labels map[string]string }
	}
	if json.Unmarshal(data, &images) != nil || len(images) != 1 {
		return release, errRelease
	}
	found := false
	for _, digest := range images[0].RepoDigests {
		if digest == release.ImageReference {
			found = true
		}
	}
	image := images[0]
	if !found || image.Config.Labels["org.opencontainers.image.version"] != g.Version || image.Config.Labels["org.opencontainers.image.revision"] != g.Revision {
		return release, errRelease
	}
	if g.Artifact != nil && image.Os+"/"+image.Architecture != g.Artifact.Platform {
		return release, errRelease
	}
	release.observedImage = true
	release.imageID = image.ID
	return release, nil
}

type TargetSpec struct {
	Project            string            `json:"project"`
	Docker             Docker            `json:"docker"`
	Release            Release           `json:"release"`
	PostgresPassword   string            `json:"postgresPassword"`
	AccessKey          string            `json:"accessKey"`
	SecretKey          string            `json:"secretKey"`
	RPCSecret          string            `json:"rpcSecret"`
	AdminToken         string            `json:"adminToken"`
	ResolverToken      string            `json:"resolverToken"`
	ReaderToken        string            `json:"readerToken,omitempty"`
	DeniedToken        string            `json:"deniedToken,omitempty"`
	ReadChecks         []ReadCheck       `json:"readChecks,omitempty"`
	RuntimeEnvironment map[string]string `json:"runtimeEnvironment,omitempty"`
	// ObjectDriver selects the object store of the target: "s3" (default, a
	// RustFS container and volume) or "filesystem" (a new private host
	// directory mounted into the Gateway; no RustFS container).
	ObjectDriver string `json:"objectDriver,omitempty"`
}

func (s TargetSpec) filesystem() bool { return s.ObjectDriver == objectstore.DriverFilesystem }

type resourceEntry struct{ kind, suffix string }

// resourceEntries lists every resource a target of this spec may own, for
// existence checks before creation and after cleanup.
func (s TargetSpec) resourceEntries() []resourceEntry {
	if s.filesystem() {
		return []resourceEntry{{"network", "network"}, {"volume", "pgdata"}, {"container", "postgres"}, {"container", "notarx"}}
	}
	return []resourceEntry{{"network", "network"}, {"volume", "pgdata"}, {"volume", "objects"}, {"container", "postgres"}, {"container", "rustfs"}, {"container", "notarx"}}
}

// ownedResourceCount is the number of resources NewTarget creates.
func (s TargetSpec) ownedResourceCount() int {
	if s.filesystem() {
		return 3
	}
	return 5
}

type ownedResource struct{ Kind, ID, Name string }

// OwnedTarget contains live ownership evidence, not an isolated:true assertion.
// Resource receipts remain in memory; failures remove only verified new IDs.
type OwnedTarget struct {
	spec      TargetSpec
	owner     string
	resources []ownedResource
	database  Postgres
	objects   *s3Objects
	directory string
	checked   bool
	// Filesystem driver: the host object root bind-mounted into the Gateway and
	// the store used to restore bytes into it until the Gateway starts.
	objectRoot string
	rootID     string
	fsStore    *objectstore.FileStore
}

func validateTargetSpec(spec TargetSpec) error {
	if !resourceName.MatchString(spec.Project) || !strings.HasPrefix(spec.Project, "notarx-restore-") || len(spec.Project) > 42 || spec.PostgresPassword == "" || spec.AdminToken == "" || spec.ResolverToken == "" || validateReadChecks(spec) != nil {
		return errors.New("explicit isolated target settings required")
	}
	switch spec.ObjectDriver {
	case "", objectstore.DriverS3:
		if spec.AccessKey == "" || spec.SecretKey == "" || spec.RPCSecret == "" || len(spec.SecretKey) < 8 || len(spec.RPCSecret) < 32 || spec.SecretKey == spec.RPCSecret {
			return errors.New("explicit isolated target settings required")
		}
	case objectstore.DriverFilesystem:
		if spec.Release.legacyEnvironment() {
			return errors.New("the filesystem object driver needs a release that supports it")
		}
	default:
		return errors.New("unsupported object driver")
	}
	if artifact := spec.Release.Identity.Artifact; artifact != nil && artifact.Kind == "binary" {
		if _, err := binaryRuntimeImage(artifact.Platform); err != nil {
			return err
		}
	}
	return nil
}

// NewTarget refuses all existing named resources, including empty projects.
// Caller validates bundle/release before invoking it. No existing target is
// adopted, cleaned, or overwritten; a repeated recovery needs a new project.
func NewTarget(ctx context.Context, spec TargetSpec) (_ *OwnedTarget, resultErr error) {
	if err := validateTargetSpec(spec); err != nil {
		return nil, err
	}
	d := spec.Docker
	if err := d.Validate(ctx); err != nil {
		return nil, err
	}
	target := &OwnedTarget{spec: spec, owner: uuid.NewString()}
	defer func() {
		if resultErr != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if target.Cleanup(cleanupCtx) != nil {
				resultErr = errors.Join(resultErr, errCleanupIncomplete)
			}
		}
	}()
	for _, entry := range spec.resourceEntries() {
		name := spec.Project + "-" + entry.suffix
		if _, err := d.output(ctx, entry.kind, "inspect", name); err == nil {
			return nil, errors.New("target resource already exists")
		}
	}
	data, err := d.output(ctx, "network", "create", "--driver", "bridge", "--label", "notarx.restore-owner="+target.owner, spec.Project+"-network")
	if err = target.recordCreated(ctx, "network", spec.Project+"-network", data, err); err != nil {
		return nil, err
	}
	volumes := []string{"pgdata", "objects"}
	if spec.filesystem() {
		volumes = []string{"pgdata"}
	}
	for _, suffix := range volumes {
		name := spec.Project + "-" + suffix
		data, err = d.output(ctx, "volume", "create", "--label", "notarx.restore-owner="+target.owner, name)
		if err = target.recordCreated(ctx, "volume", name, data, err); err != nil {
			return nil, err
		}
		if target.checkResource(ctx, target.resources[len(target.resources)-1]) != nil {
			return nil, errors.New("target volume ownership unavailable")
		}
	}
	label := "notarx.restore-owner=" + target.owner
	start := func(name, image string, environment, tail []string) error {
		args := []string{"create", "--name", spec.Project + "-" + name, "--network", spec.Project + "-network", "--network-alias", name, "--label", label, "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true"}
		if name == "postgres" {
			args = append(args, "--cap-add", "CHOWN", "--cap-add", "SETUID", "--cap-add", "SETGID", "--cap-add", "FOWNER", "--cap-add", "DAC_OVERRIDE")
		}
		if name == "postgres" {
			args = append(args, "-p", "127.0.0.1::5432", "-v", spec.Project+"-pgdata:/var/lib/postgresql")
		}
		if name == "rustfs" {
			args = append(args, "-p", "127.0.0.1::9000", "-v", spec.Project+"-objects:/data")
		}
		for _, e := range environment {
			key, _, _ := strings.Cut(e, "=")
			args = append(args, "--env", key)
		}
		args = append(args, image)
		args = append(args, tail...)
		out, err := d.command(ctx, environment, args...).Output()
		if err = target.recordCreated(ctx, "container", spec.Project+"-"+name, out, err); err != nil {
			return err
		}
		if _, err = d.output(ctx, "start", target.resourceID("container", spec.Project+"-"+name)); err != nil {
			return errors.New("target service startup failed")
		}
		return nil
	}
	// PostgreSQL needs its image's normal capability set during data-directory
	// initialization; a separate user-owned volume remains its only writable mount.
	pgEnv := []string{"POSTGRES_DB=notarx", "POSTGRES_USER=notarx", "POSTGRES_PASSWORD=" + spec.PostgresPassword}
	if err = start("postgres", postgresImage, pgEnv, []string{"postgres", "-c", "shared_preload_libraries=pg_stat_statements"}); err != nil {
		return nil, err
	}
	if spec.filesystem() {
		// A new private directory owned by this user is the object root; the
		// restored Gateway mounts it and runs as the same user.
		if target.objectRoot, err = os.MkdirTemp("", "notarx-restore-objects-"); err != nil {
			return nil, errors.New("object root unavailable")
		}
	} else {
		rustEnv := []string{"RUSTFS_ACCESS_KEY=" + spec.AccessKey, "RUSTFS_SECRET_KEY=" + spec.SecretKey, "RUSTFS_RPC_SECRET=" + spec.RPCSecret, "RUSTFS_CONSOLE_ENABLE=false", "RUSTFS_VOLUMES=/data", "RUSTFS_ADDRESS=0.0.0.0:9000"}
		if err = start("rustfs", rustfsImage, rustEnv, []string{"rustfs"}); err != nil {
			return nil, err
		}
	}
	pgPort, err := target.port(ctx, "postgres", "5432/tcp")
	if err != nil {
		return nil, err
	}
	target.database = Postgres{DSN: fmt.Sprintf("postgres://notarx:%s@127.0.0.1:%s/notarx?sslmode=disable", escapePassword(spec.PostgresPassword), pgPort), ToolsContainer: target.resourceID("container", spec.Project+"-postgres"), DockerContext: d.Context}
	if !spec.filesystem() {
		s3Port, portErr := target.port(ctx, "rustfs", "9000/tcp")
		if portErr != nil {
			return nil, portErr
		}
		target.objects, err = newS3(S3Settings{Endpoint: "http://127.0.0.1:" + s3Port, Bucket: "notarx-cache", AccessKey: spec.AccessKey, SecretKey: spec.SecretKey})
		if err != nil {
			return nil, err
		}
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 3*time.Second)
		pgReady := target.database.Empty(attemptCtx)
		var objectsReady error
		if target.objects != nil {
			objectsReady = target.objects.store.EnsureBucket(attemptCtx)
		}
		attemptCancel()
		if pgReady == nil && objectsReady == nil {
			break
		}
		if time.Now().After(deadline) {
			if pgReady != nil {
				return nil, errors.New("isolated database readiness failed")
			}
			return nil, errors.New("isolated object store readiness failed")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	if err = target.CheckEmpty(ctx); err != nil {
		return nil, err
	}
	return target, nil
}
func (t *OwnedTarget) checkResource(ctx context.Context, r ownedResource) error {
	data, err := t.spec.Docker.output(ctx, r.Kind, "inspect", r.ID)
	if err != nil {
		return err
	}
	var entries []struct {
		ID       string `json:"Id"`
		Name     string
		Labels   map[string]string
		Config   struct{ Labels map[string]string }
		Driver   string
		Scope    string
		Internal bool
		Options  map[string]string
		Mounts   []struct {
			Type, Name, Source, Destination string
			RW                              bool
		}
		NetworkSettings struct {
			Networks map[string]struct{ NetworkID string }
		}
		HostConfig struct{ NetworkMode string }
		State      struct{ Status string }
	}
	if json.Unmarshal(data, &entries) != nil || len(entries) != 1 {
		return errors.New("ownership unavailable")
	}
	item := entries[0]
	labels := item.Labels
	if r.Kind == "container" {
		labels = item.Config.Labels
	}
	if labels["notarx.restore-owner"] != t.owner {
		return errors.New("foreign target resource")
	}
	if r.Kind != "volume" && (item.ID != r.ID || strings.TrimPrefix(item.Name, "/") != r.Name) {
		return errors.New("target identity changed")
	}
	if r.Kind == "network" && (item.Driver != "bridge" || item.Scope != "local" || item.Internal || !localBridgeOptions(item.Options)) {
		return errors.New("target network not isolated")
	}
	if r.Kind == "volume" && (item.Name != r.Name || item.Driver != "local" || len(item.Options) != 0) {
		return errors.New("target volume not local")
	}
	if r.Kind == "container" {
		networkID := t.resourceID("network", t.spec.Project+"-network")
		if networkID == "" {
			if item.HostConfig.NetworkMode != "none" || len(item.Mounts) != 0 {
				return errors.New("helper not isolated")
			}
		} else {
			networkName := t.spec.Project + "-network"
			endpoint, configured := item.NetworkSettings.Networks[networkName]
			attached := endpoint.NetworkID
			// Docker has not allocated NetworkID for a created container yet.
			// Permit only its exact owned configured network before first start.
			unstarted := item.State.Status == "created" && item.HostConfig.NetworkMode == networkName && attached == ""
			if !configured || len(item.NetworkSettings.Networks) != 1 || (attached != networkID && !unstarted) {
				return errors.New("target network changed")
			}
			switch r.Name {
			case t.spec.Project + "-postgres", t.spec.Project + "-rustfs":
				suffix, destination := "pgdata", "/var/lib/postgresql"
				if r.Name == t.spec.Project+"-rustfs" {
					suffix, destination = "objects", "/data"
				}
				if len(item.Mounts) != 1 || item.Mounts[0].Type != "volume" || item.Mounts[0].Name != t.resourceID("volume", t.spec.Project+"-"+suffix) || item.Mounts[0].Destination != destination || !item.Mounts[0].RW {
					return errors.New("target mount changed")
				}
			case t.spec.Project + "-notarx":
				want := 0
				if t.spec.Release.Identity.Artifact != nil && t.spec.Release.Identity.Artifact.Kind == "binary" {
					want++
				}
				if t.spec.filesystem() {
					want++
				}
				if len(item.Mounts) != want {
					return errors.New("unexpected runtime mount")
				}
				for _, mount := range item.Mounts {
					switch {
					case mount.Type == "bind" && mount.Destination == "/notarx" && !mount.RW && t.directory != "" && filepath.Clean(mount.Source) == filepath.Join(t.directory, "notarx"):
					case t.spec.filesystem() && mount.Type == "bind" && mount.Destination == fsObjectMount && mount.RW && t.objectRoot != "" && filepath.Clean(mount.Source) == t.objectRoot:
					default:
						return errors.New("runtime mount changed")
					}
				}
			default:
				return errors.New("unknown target service")
			}
		}
	}
	return nil
}
func (t *OwnedTarget) resourceID(kind, name string) string {
	for _, r := range t.resources {
		if r.Kind == kind && r.Name == name {
			return r.ID
		}
	}
	return ""
}
func (t *OwnedTarget) checkOwned(ctx context.Context) error {
	for _, r := range t.resources {
		if t.checkResource(ctx, r) != nil {
			return errors.New("target ownership changed")
		}
	}
	return nil
}
func (t *OwnedTarget) port(ctx context.Context, service, port string) (string, error) {
	id := t.resourceID("container", t.spec.Project+"-"+service)
	if id == "" || t.checkOwned(ctx) != nil {
		return "", errors.New("target ownership changed")
	}
	data, err := t.spec.Docker.output(ctx, "inspect", id)
	if err != nil {
		return "", err
	}
	var entries []struct {
		NetworkSettings struct {
			Ports map[string][]struct{ HostIP, HostPort string }
		}
	}
	if json.Unmarshal(data, &entries) != nil || len(entries) != 1 {
		return "", errors.New("target port unavailable")
	}
	bindings := entries[0].NetworkSettings.Ports[port]
	if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || bindings[0].HostPort == "" {
		return "", errors.New("target port not loopback")
	}
	return bindings[0].HostPort, nil
}
func (t *OwnedTarget) CheckEmpty(ctx context.Context) error {
	if len(t.resources) != t.spec.ownedResourceCount() {
		return errors.New("target ownership incomplete")
	}
	for _, r := range t.resources {
		if t.checkResource(ctx, r) != nil {
			return errors.New("target ownership changed")
		}
	}
	if t.database.Empty(ctx) != nil || t.objectsEmpty(ctx) != nil {
		return errors.New("target not empty")
	}
	t.checked = true
	return nil
}
func (t *OwnedTarget) RestoreDatabase(ctx context.Context, r io.Reader) error {
	if !t.checked || t.checkOwned(ctx) != nil {
		return errors.New("target not checked")
	}
	return t.database.RestoreDatabase(ctx, r)
}
func (t *OwnedTarget) Ledger(ctx context.Context) ([]byte, error)   { return t.database.Ledger(ctx) }
func (t *OwnedTarget) Metadata(ctx context.Context) ([]byte, error) { return t.database.Metadata(ctx) }
func (t *OwnedTarget) Put(ctx context.Context, key string, r io.ReadSeeker, size int64, digest string) error {
	if !t.checked || t.checkOwned(ctx) != nil {
		return errors.New("target not checked")
	}
	if t.spec.filesystem() {
		store, err := t.openFilesystem()
		if err != nil {
			return err
		}
		return store.PutVerifiedReader(ctx, key, r, size, digest)
	}
	return t.objects.Put(ctx, key, r, size, digest)
}
func (t *OwnedTarget) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if t.spec.filesystem() {
		store, err := t.openFilesystem()
		if err != nil {
			return nil, 0, err
		}
		return store.Open(ctx, key)
	}
	return t.objects.Open(ctx, key)
}

// SetObjectRootID makes the new filesystem root carry the identity recorded in
// the restored database. It must run before the first object is written.
func (t *OwnedTarget) SetObjectRootID(rootID string) error {
	if !t.spec.filesystem() {
		// An S3 target has no root to identify: nothing to carry over.
		return nil
	}
	if t.fsStore != nil {
		return errors.New("object root is already initialised")
	}
	t.rootID = rootID
	_, err := t.openFilesystem()
	return err
}

// openFilesystem opens (initialising on first use) the host object root.
func (t *OwnedTarget) openFilesystem() (*objectstore.FileStore, error) {
	if t.fsStore != nil {
		return t.fsStore, nil
	}
	if t.objectRoot == "" {
		return nil, errors.New("object root unavailable")
	}
	cfg := objectstore.Defaults()
	cfg.Driver, cfg.FSRoot = objectstore.DriverFilesystem, t.objectRoot
	var store *objectstore.FileStore
	var err error
	if t.rootID != "" {
		store, err = objectstore.NewFileStoreWithRootID(cfg, t.rootID)
	} else {
		store, err = objectstore.NewFileStore(cfg)
	}
	if err != nil {
		return nil, errors.New("object root unavailable")
	}
	t.fsStore = store
	return store, nil
}

// closeFilesystem releases the host handle so the Gateway can take the root.
func (t *OwnedTarget) closeFilesystem() error {
	if t.fsStore == nil {
		return nil
	}
	err := t.fsStore.Close()
	t.fsStore = nil
	return err
}

func (t *OwnedTarget) objectsEmpty(ctx context.Context) error {
	if !t.spec.filesystem() {
		return t.objects.Empty(ctx)
	}
	entries, err := os.ReadDir(t.objectRoot)
	if t.objectRoot == "" || err != nil || len(entries) != 0 {
		return errors.New("object root not empty")
	}
	return nil
}

// Cleanup refuses foreign or replaced resources; it never runs project-wide
// compose down, deletes a bucket on an existing endpoint, or removes source data.
func (t *OwnedTarget) Cleanup(ctx context.Context) error {
	var problems []error
	for i := len(t.resources) - 1; i >= 0; i-- {
		r := t.resources[i]
		if t.checkResource(ctx, r) != nil {
			problems = append(problems, errors.New("cleanup ownership unavailable"))
			continue
		}
		args := []string{r.Kind, "rm", r.ID}
		if r.Kind == "container" {
			args = []string{"rm", "-f", r.ID}
		}
		if _, err := t.spec.Docker.output(ctx, args...); err != nil {
			problems = append(problems, err)
		}
	}
	if err := t.closeFilesystem(); err != nil {
		problems = append(problems, errors.New("object root close failed"))
	}
	for _, directory := range []string{t.directory, t.objectRoot} {
		if directory == "" {
			continue
		}
		if err := os.RemoveAll(directory); err != nil {
			problems = append(problems, errors.New("private stage cleanup failed"))
		}
	}
	return errors.Join(problems...)
}

func (t *OwnedTarget) StartGateway(ctx context.Context) (string, error) {
	if !t.checked {
		return "", errors.New("target not checked")
	}
	spec := t.spec
	image := spec.Release.ImageReference
	if artifact := spec.Release.Identity.Artifact; artifact != nil && artifact.Kind == "binary" {
		var err error
		image, err = binaryRuntimeImage(artifact.Platform)
		if err != nil {
			return "", err
		}
	}
	for _, r := range t.resources {
		if t.checkResource(ctx, r) != nil {
			return "", errors.New("target ownership changed")
		}
	}
	if err := t.closeFilesystem(); err != nil {
		return "", errors.New("object root unavailable")
	}
	args := []string{"create", "--name", spec.Project + "-notarx", "--network", spec.Project + "-network", "--label", "notarx.restore-owner=" + t.owner, "-p", "127.0.0.1::8080", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=256m,mode=1777", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true"}
	if g := spec.Release.Identity; g.Artifact != nil && g.Artifact.Kind == "binary" {
		directory, err := os.MkdirTemp("", "notarx-restore-release-")
		if err != nil {
			return "", errors.New("release staging unavailable")
		}
		t.directory = directory
		input, err := os.Open(releaseBinaryPath(spec.Release.Directory))
		if err != nil {
			return "", err
		}
		defer func() { _ = input.Close() }()
		output, err := os.OpenFile(filepath.Join(directory, "notarx"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0500)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(output, input)
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil {
			return "", errors.New("release staging failed")
		}
		sum, _, err := describeBinary(filepath.Join(directory, "notarx"))
		if err != nil || sum != g.Artifact.SHA256 {
			return "", errRelease
		}
		// Nonroot in the runtime can execute the verified mounted file; no parent
		// directory or writable runtime configuration is mounted into the container.
		if os.Chmod(filepath.Join(directory, "notarx"), 0555) != nil {
			return "", errors.New("release staging failed")
		}
		args = append(args, "--mount", "type=bind,source="+filepath.Join(directory, "notarx")+",target=/notarx,readonly", "--entrypoint", "/notarx")
	}
	objectEnvironment := []string{"NOTARX_OBJECT_STORE_ENDPOINT=http://rustfs:9000", "NOTARX_OBJECT_STORE_AUTO_CREATE_BUCKET=true", "NOTARX_OBJECT_STORE_BUCKET=notarx-cache", "NOTARX_OBJECT_STORE_ACCESS_KEY=" + spec.AccessKey, "NOTARX_OBJECT_STORE_SECRET_KEY=" + spec.SecretKey}
	if spec.filesystem() {
		if t.objectRoot == "" {
			return "", errors.New("object root unavailable")
		}
		// The Gateway runs as the user that owns the root, so it can write it.
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--mount", "type=bind,source="+t.objectRoot+",target="+fsObjectMount)
		objectEnvironment = []string{"NOTARX_OBJECT_STORE_DRIVER=" + objectstore.DriverFilesystem, "NOTARX_OBJECT_STORE_FS_ROOT=" + fsObjectMount}
	}
	environment := append([]string{"NOTARX_DATABASE_URL=postgres://notarx:" + escapePassword(spec.PostgresPassword) + "@postgres:5432/notarx?sslmode=disable"}, objectEnvironment...)
	environment = append(environment, "NOTARX_ADMIN_TOKEN="+spec.AdminToken, "NOTARX_RESOLVER_TOKEN="+spec.ResolverToken, "NOTARX_NODE_ROLES=api", "NOTARX_INSTANCE_ID="+spec.Project, "NOTARX_LOG_BUFFER_LINES=1000")
	legacyEnv := spec.Release.legacyEnvironment()
	for i, e := range environment {
		key, value, _ := strings.Cut(e, "=")
		if legacyEnv {
			key = legacyEnvironmentKey(key)
			environment[i] = key + "=" + value
		}
		args = append(args, "--env", key)
	}
	for key, value := range spec.RuntimeEnvironment {
		if !allowedRuntimeKey(key) {
			return "", errors.New("unsupported private runtime setting")
		}
		if legacyEnv {
			key = legacyEnvironmentKey(key)
		}
		environment = append(environment, key+"="+value)
		args = append(args, "--env", key)
	}
	if g := spec.Release.Identity; g.Artifact != nil {
		args = append(args, "--platform", g.Artifact.Platform)
	}
	args = append(args, image)
	out, err := spec.Docker.command(ctx, environment, args...).Output()
	if err = t.recordCreated(ctx, "container", spec.Project+"-notarx", out, err); err != nil {
		return "", err
	}
	if _, err = spec.Docker.output(ctx, "start", t.resourceID("container", spec.Project+"-notarx")); err != nil {
		return "", errors.New("restored Gateway startup failed")
	}
	port, err := t.port(ctx, "notarx", "8080/tcp")
	if err != nil {
		return "", err
	}
	return "http://127.0.0.1:" + port, nil
}
func describeBinary(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	return hashReader(f)
}

// ValidateDump reads and decompresses the complete archive to discarded SQL,
// without restoring it or selecting a target database. The temporary PG tool container has no network or mounts.
func (d Docker) ValidateDump(ctx context.Context, r io.Reader) (resultErr error) {
	owner := uuid.NewString()
	name := "notarx-dump-check-" + strings.ReplaceAll(owner, "-", "")
	data, err := d.output(ctx, "create", "-i", "--name", name, "--label", "notarx.restore-owner="+owner, "--network", "none", "--read-only", "--tmpfs", "/var/lib/postgresql:rw,noexec,nosuid,size=1m", "--cap-drop", "ALL", postgresImage, "pg_restore", "--file=-", "--no-owner", "--no-privileges")
	helper := &OwnedTarget{spec: TargetSpec{Docker: d}, owner: owner}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if helper.Cleanup(cleanupCtx) != nil {
			resultErr = errors.Join(resultErr, errCleanupIncomplete)
		}
	}()
	if err = helper.recordCreated(ctx, "container", name, data, err); err != nil {
		return err
	}
	cmd := d.command(ctx, nil, "start", "-a", "-i", helper.resources[0].ID)
	cmd.Stdin = r
	cmd.Stdout = io.Discard
	if cmd.Run() != nil {
		return errors.New("database archive unsupported or corrupt")
	}
	return nil
}

var _ Destination = (*OwnedTarget)(nil)

func escapePassword(password string) string {
	return url.UserPassword("notarx", password).String()[len("notarx:"):]
}

func allowedRuntimeKey(key string) bool {
	switch key {
	case "NOTARX_LEGACY_READ_DEFAULT", "NOTARX_REPOSITORY_READERS", "NOTARX_SETTINGS_ENCRYPTION_KEY", "NOTARX_SETTINGS_ENCRYPTION_KEY_PREVIOUS", "NOTARX_EGRESS_PROXY_KEY", "NOTARX_OIDC_ISSUER", "NOTARX_OIDC_AUDIENCE", "NOTARX_OIDC_JWKS_URL", "NOTARX_OIDC_CLIENT_ID", "NOTARX_OIDC_CLIENT_SECRET", "NOTARX_OIDC_REDIRECT_URL", "NOTARX_OIDC_SCOPES", "NOTARX_OIDC_ADMIN_SUBJECTS", "NOTARX_OIDC_MEMBER_ROLES", "NOTARX_OIDC_ADMIN_ROLES":
		return true
	}
	return false
}

// Docker 29 records these default bridge options even when none were supplied.
func localBridgeOptions(options map[string]string) bool {
	for key, value := range options {
		if key == "com.docker.network.enable_ipv4" && value == "true" {
			continue
		}
		if key == "com.docker.network.enable_ipv6" && value == "false" {
			continue
		}
		return false
	}
	return true
}

// Record receipts before startup, including a lost create response. A unique
// invocation label and exact new name identify only this invocation's resource.
// Foreign resources are never adopted. Cleanup still rechecks all boundaries.
func (t *OwnedTarget) recordCreated(ctx context.Context, kind, name string, out []byte, createErr error) error {
	id := strings.TrimSpace(string(out))
	if createErr != nil || id == "" {
		observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		data, e := t.spec.Docker.output(observeCtx, kind, "inspect", name)
		var items []struct {
			ID     string `json:"Id"`
			Name   string
			Labels map[string]string
			Config struct{ Labels map[string]string }
		}
		if e != nil || json.Unmarshal(data, &items) != nil || len(items) != 1 {
			return errors.New("resource creation outcome unavailable")
		}
		item := items[0]
		labels := item.Labels
		if kind == "container" {
			labels = item.Config.Labels
		}
		if labels["notarx.restore-owner"] != t.owner || strings.TrimPrefix(item.Name, "/") != name {
			return errors.New("resource creation failed")
		}
		id = item.ID
		if kind == "volume" {
			id = item.Name
		}
	}
	if (kind == "volume" && id != name) || (kind != "volume" && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id)) {
		return errors.New("resource receipt invalid")
	}
	t.resources = append(t.resources, ownedResource{kind, id, name})
	if createErr != nil {
		return errors.New("resource creation failed")
	}
	return nil
}
