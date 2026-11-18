package objectstore

import "testing"

func TestDigestMetadataReadsNotarxKeyOnly(t *testing.T) {
	for _, key := range []string{"notarx-sha256", "Notarx-Sha256", "X-Amz-Meta-Notarx-Sha256"} {
		if got := digestMetadata(map[string]string{key: "sha256:abc"}); got != "sha256:abc" {
			t.Errorf("digestMetadata(%q) = %q, want the digest", key, got)
		}
	}
	legacy := "artifact" + "-gateway-sha256"
	if got := digestMetadata(map[string]string{legacy: "sha256:abc"}); got != "" {
		t.Errorf("digestMetadata(legacy key) = %q, want no digest", got)
	}
}
