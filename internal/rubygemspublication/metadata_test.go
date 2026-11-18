package rubygemspublication

import "testing"

func TestValidInputRejectsNonObjectMetadataJSON(t *testing.T) {
	for _, bad := range []string{`[]`, `null`, `"x"`, `{`, ``} {
		if ValidInput(Session{MetadataJSON: []byte(bad)}) {
			t.Errorf("metadata %q was accepted", bad)
		}
	}
}
