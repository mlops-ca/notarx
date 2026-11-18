package repository

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These tests replace the jsonb_typeof(metadata_json::jsonb) = 'object' CHECK
// constraints that were dropped when metadata_json became jsonb (ADR 0023 section 9):
// the session validators reject a document that is not one JSON object or is too big.

func helmSessionWith(metadata string) HelmSession {
	return HelmSession{ID: uuid.NewString(), RepositoryID: uuid.NewString(), Name: "acme", Version: "1.0.0", Digest: "sha256:" + fmt.Sprintf("%064x", 1), Size: 60, Actor: "publisher", MetadataJSON: []byte(metadata), ChartYAML: []byte("name: acme\n"), ExpiresAt: time.Now().Add(time.Hour)}
}

func nuGetStagingWith(metadata string) NuGetSession {
	return NuGetSession{ID: uuid.NewString(), RepositoryID: uuid.NewString(), PackageID: "acme", Version: "1.0.0", Digest: "sha256:" + fmt.Sprintf("%064x", 1), Size: 60, Actor: "publisher", MetadataJSON: []byte(metadata), Nuspec: []byte("<package/>"), ExpiresAt: time.Now().Add(time.Hour)}
}

func TestHelmSessionMetadataMustBeAnObjectWithinTheLimit(t *testing.T) {
	if !validHelmSession(helmSessionWith(`{"name":"acme","version":"1.0.0"}`)) {
		t.Fatal("a valid object was rejected")
	}
	for _, bad := range []string{`[]`, `null`, `"acme"`, `7`, `{"name":"acme","version":"1.0.0"`, ``, `{"name":"acme","version":"1.0.0","pad":"` + strings.Repeat("x", MaxMetadataJSONBytes) + `"}`} {
		if validHelmSession(helmSessionWith(bad)) {
			t.Errorf("metadata %.40q was accepted", bad)
		}
	}
}

func TestNuGetStagingMetadataMustBeAnObjectWithinTheLimit(t *testing.T) {
	if !validNuGetStaging(nuGetStagingWith(`{"id":"acme","version":"1.0.0"}`)) {
		t.Fatal("a valid object was rejected")
	}
	for _, bad := range []string{`[]`, `null`, `"acme"`, `7`, `{"id":"acme"`, ``, `{"id":"acme","version":"1.0.0","pad":"` + strings.Repeat("x", MaxMetadataJSONBytes) + `"}`} {
		if validNuGetStaging(nuGetStagingWith(bad)) {
			t.Errorf("metadata %.40q was accepted", bad)
		}
	}
	empty := nuGetStagingWith(`{"id":"acme","version":"1.0.0"}`)
	empty.Nuspec = nil
	if validNuGetStaging(empty) {
		t.Error("a staged session without a nuspec was accepted (the remaining nuspec CHECK)")
	}
}
