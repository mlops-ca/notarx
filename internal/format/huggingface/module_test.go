package huggingface

import (
	"database/sql"
	"strings"
	"testing"

	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/repository"
)

const commitSHA = "0123456789abcdef0123456789abcdef01234567"

func TestValidCoordinateAcceptsOnlyTheCommitForm(t *testing.T) {
	module := &Module{}
	valid := []string{
		"model/acme/widget@" + commitSHA,
		"dataset/acme/data.v2@" + commitSHA,
		"model/A1/b_c-d@" + commitSHA,
	}
	for _, coordinate := range valid {
		if !module.ValidCoordinate(coordinate) {
			t.Errorf("%q rejected", coordinate)
		}
	}
	invalid := []string{
		"",
		"model/acme/widget",                    // no commit
		"model/acme/widget@main",               // a ref
		"model/acme/widget@v1.0",               // a tag
		"model/acme/widget@" + commitSHA[:39],  // short sha
		"model/acme/widget@" + commitSHA + "0", // long sha
		"model/acme/widget@" + strings.ToUpper(commitSHA),
		"space/acme/widget@" + commitSHA,      // Spaces are not supported
		"acme/widget@" + commitSHA,            // no repo type
		"model/widget@" + commitSHA,           // no org
		"model/acme/team/widget@" + commitSHA, // too deep
		"model/acme//widget@" + commitSHA,
		"model/../widget@" + commitSHA,
		"model/acme/wid get@" + commitSHA,
		"model/-acme/widget@" + commitSHA,
		"model/acme/widget-@" + commitSHA,
		"model/acme/wid--get@" + commitSHA,
		"model/acme/wid..get@" + commitSHA,
		"model/acme/" + strings.Repeat("a", 97) + "@" + commitSHA,
		"model/acme/widget@" + commitSHA + "@" + commitSHA,
	}
	for _, coordinate := range invalid {
		if module.ValidCoordinate(coordinate) {
			t.Errorf("%q accepted", coordinate)
		}
	}
}

func TestParseCoordinateRoundTrips(t *testing.T) {
	parsed, ok := ParseCoordinate("dataset/acme/data@" + commitSHA)
	if !ok || parsed.RepoType != RepoTypeDataset || parsed.Name != "acme/data" || parsed.Commit != commitSHA {
		t.Fatalf("parsed = %+v, %v", parsed, ok)
	}
	if parsed.String() != "dataset/acme/data@"+commitSHA {
		t.Fatalf("String = %q", parsed.String())
	}
}

func TestValidPath(t *testing.T) {
	for _, ok := range []string{"config.json", "a/b/c.safetensors", "ünï/cødé.txt", strings.Repeat("a", MaxPathLength)} {
		if !ValidPath(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "/abs", "dir/", "a//b", "./a", "a/../b", "a\\b", "a\x00b", "a\nb", strings.Repeat("a", MaxPathLength+1)} {
		if ValidPath(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	// The limit counts characters, not bytes: 512 multi-byte characters still fit the key.
	if !ValidPath(strings.Repeat("é", MaxPathLength)) {
		t.Error("a path of 512 two-byte characters was rejected")
	}
}

func TestDigestAndCommitShapes(t *testing.T) {
	if !ValidDigest(strings.Repeat("a", 64)) || ValidDigest(strings.Repeat("A", 64)) || ValidDigest("abc") {
		t.Error("digest validation")
	}
	if !ValidCommit(commitSHA) || ValidCommit("main") {
		t.Error("commit validation")
	}
	if ValidRepoName("noorg") || !ValidRepoName("org/name") || ValidRepoName("a/b/c") {
		t.Error("repo name validation")
	}
}

func TestModuleIdentityAndCapabilities(t *testing.T) {
	module, err := New(Options{Store: NewStore(&sql.DB{}, Postgres)})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := formatmodule.New(module)
	if err != nil {
		t.Fatal(err)
	}
	if module.Format() != repository.FormatHuggingFace {
		t.Fatalf("format = %q", module.Format())
	}
	if got := registry.Formats(); len(got) != 1 || got[0] != "huggingface" {
		t.Fatalf("formats = %v", got)
	}
	if _, ok := formatmodule.Capability[formatmodule.Profile](registry, "huggingface"); !ok {
		t.Error("Profile capability missing")
	}
	if _, ok := formatmodule.Capability[formatmodule.Governance](registry, "huggingface"); !ok {
		t.Error("Governance capability missing")
	}
	if _, ok := formatmodule.Capability[formatmodule.Browse](registry, "huggingface"); !ok {
		t.Error("Browse capability missing")
	}
	if _, ok := formatmodule.Capability[formatmodule.Usage](registry, "huggingface"); !ok {
		t.Error("Usage capability missing")
	}
	if _, ok := formatmodule.Capability[formatmodule.NativeRoutes](registry, "huggingface"); ok {
		t.Error("the skeleton must not mount routes yet")
	}
	profile := module.Profile()
	if profile.Format != repository.FormatHuggingFace || !profile.GroupSupported || len(profile.RepositoryTypes) != 2 {
		t.Fatalf("profile = %+v", profile)
	}
	if module.UsageKey("model/a/b@"+commitSHA) != "model/a/b@"+commitSHA {
		t.Error("UsageKey")
	}
}

func TestNewRequiresAStore(t *testing.T) {
	var absent *Store
	for _, options := range []Options{{}, {Store: absent}, {Store: &Store{}}} {
		if _, err := New(options); err == nil {
			t.Fatalf("New(%+v) accepted a missing store", options)
		}
	}
}

func TestGovernanceMutationsAreDisabledUntilHostedExists(t *testing.T) {
	module := &Module{}
	if err := module.Tombstone(nil, "r", "c"); err != repository.ErrDisabled { //nolint:staticcheck // a nil context is never read
		t.Fatalf("Tombstone = %v", err)
	}
	if err := module.Restore(nil, "r", "c"); err != repository.ErrDisabled { //nolint:staticcheck
		t.Fatalf("Restore = %v", err)
	}
}

func TestBrowseValidation(t *testing.T) {
	module := &Module{}
	if !module.ValidBrowseParent(repository.ArtifactBrowseParent{Kind: repository.BrowseNodeDirectory, Path: "model/acme"}) ||
		module.ValidBrowseParent(repository.ArtifactBrowseParent{Kind: repository.BrowseNodeDirectory, Path: "/model"}) ||
		module.ValidBrowseParent(repository.ArtifactBrowseParent{Kind: repository.BrowseNodeDirectory}) {
		t.Error("ValidBrowseParent")
	}
	if !module.ValidPrefix("model/acme/") || module.ValidPrefix("/model") || module.ValidPrefix("a\\b") || module.ValidPrefix("danger\u202e") || module.ValidPrefix(strings.Repeat("a", 513)) {
		t.Error("ValidPrefix")
	}
	if !module.ValidKeyword("widget", repository.ArtifactSearchContains) || module.ValidKeyword("danger\u202e", repository.ArtifactSearchContains) || module.ValidKeyword("/x", repository.ArtifactSearchPrefix) {
		t.Error("ValidKeyword")
	}
	query := repository.ArtifactSearchQuery{Value: "x"}
	if module.SearchQuery(query) != query || module.BrowseName("a") != "a" {
		t.Error("SearchQuery or BrowseName")
	}
}

func TestStoreRebindNumbersPlaceholdersForPostgresOnly(t *testing.T) {
	postgres, mysql := NewStore(nil, Postgres), NewStore(nil, MySQL)
	if got := postgres.rebind("SELECT ? , ? WHERE x = ?"); got != "SELECT $1 , $2 WHERE x = $3" {
		t.Errorf("postgres rebind = %q", got)
	}
	if got := mysql.rebind("SELECT ? , ?"); got != "SELECT ? , ?" {
		t.Errorf("mysql rebind = %q", got)
	}
	if got := likeEscape("a%b_c!d"); got != "a!%b!_c!!d" {
		t.Errorf("likeEscape = %q", got)
	}
}

func TestStoreValidatesBeforeTouchingTheDatabase(t *testing.T) {
	store := NewStore(nil, Postgres)
	if _, err := store.EnsureRepo(nil, "not-a-uuid", RepoTypeModel, "a/b"); err != ErrInvalid { //nolint:staticcheck
		t.Fatalf("EnsureRepo = %v", err)
	}
	if err := store.PutRef(nil, Ref{Kind: RefBranch, Name: "main", CommitSHA: "main"}); err != ErrInvalid { //nolint:staticcheck
		t.Fatalf("PutRef = %v", err)
	}
	if err := store.CommitBlob(nil, "r", "h", "c", "p", "short", 1); err != ErrInvalid { //nolint:staticcheck
		t.Fatalf("CommitBlob = %v", err)
	}
	if files, err := store.ListFiles(nil, "c", "", 0); files != nil || err != nil { //nolint:staticcheck
		t.Fatalf("ListFiles(0) = %v, %v", files, err)
	}
}
