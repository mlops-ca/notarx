package backupops

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/objectstore"
)

func filesystemTargetSpec() TargetSpec {
	return TargetSpec{Project: "notarx-restore-fstest0001", ObjectDriver: objectstore.DriverFilesystem, PostgresPassword: "p", AdminToken: "a", ResolverToken: "r"}
}

func TestFilesystemTargetSpecNeedsNoS3Settings(t *testing.T) {
	spec := filesystemTargetSpec()
	if err := validateTargetSpec(spec); err != nil {
		t.Fatal(err)
	}
	if spec.ownedResourceCount() != 3 {
		t.Fatalf("owned resources = %d, want network, pgdata and postgres", spec.ownedResourceCount())
	}
	for _, entry := range spec.resourceEntries() {
		if entry.suffix == "rustfs" || entry.suffix == "objects" {
			t.Fatalf("filesystem target lists %s", entry.suffix)
		}
	}
	s3 := TargetSpec{Project: spec.Project, PostgresPassword: "p", AdminToken: "a", ResolverToken: "r"}
	if validateTargetSpec(s3) == nil {
		t.Fatal("an S3 target without credentials was accepted")
	}
	if s3.ownedResourceCount() != 5 || len(s3.resourceEntries()) != 6 {
		t.Fatal("S3 target resources changed")
	}
	spec.ObjectDriver = "bogus"
	if validateTargetSpec(spec) == nil {
		t.Fatal("unknown object driver accepted")
	}
}

func TestFilesystemTargetRestoresIntoOwnedRootWithRecordedIdentity(t *testing.T) {
	root := t.TempDir()
	target := &OwnedTarget{spec: filesystemTargetSpec(), objectRoot: root, checked: true}
	if err := target.objectsEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	rootID := uuid.NewString()
	if err := target.SetObjectRootID(rootID); err != nil {
		t.Fatal(err)
	}
	if target.SetObjectRootID(rootID) == nil {
		t.Fatal("root ID set twice")
	}
	body := []byte("restored bytes")
	digest, _, _ := hashReader(bytes.NewReader(body))
	if err := target.Put(context.Background(), "native/raw/sha256/x", bytes.NewReader(body), int64(len(body)), digest); err != nil {
		t.Fatal(err)
	}
	reader, size, err := target.Open(context.Background(), "native/raw/sha256/x")
	if err != nil || size != int64(len(body)) {
		t.Fatalf("open: %v %d", err, size)
	}
	_ = reader.Close()
	if target.objectsEmpty(context.Background()) == nil {
		t.Fatal("root with objects reported empty")
	}
	if err = target.closeFilesystem(); err != nil {
		t.Fatal(err)
	}
	objects, err := NewFilesystemObjects(FilesystemSettings{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = objects.Close() }()
	if objects.RootID() != rootID {
		t.Fatalf("root ID = %s, want the recorded %s", objects.RootID(), rootID)
	}
	if err = target.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatal("cleanup kept the object root")
	}
}

// A backup exported from a filesystem source carries a root ID; restoring it
// into an S3 target must not fail on it.
func TestS3TargetIgnoresRootID(t *testing.T) {
	target := &OwnedTarget{spec: TargetSpec{Project: "notarx-restore-s3test00001"}}
	if err := target.SetObjectRootID(uuid.NewString()); err != nil {
		t.Fatalf("err = %v", err)
	}
}
