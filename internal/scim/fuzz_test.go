package scim

import "testing"

// FuzzParseFilter feeds arbitrary filters to the parser. It must never panic, and whatever it accepts must be one
// of the supported comparisons with a non-empty value; the committed corpus under testdata/fuzz runs as a regular
// test, and make scim-fuzz explores beyond it.
func FuzzParseFilter(f *testing.F) {
	for _, seed := range []string{
		`userName eq "alice@example.com"`, `externalId eq "x"`, `displayName eq "Eng"`, `userName ne "a"`, `userName eq "a" and active eq true`,
		`emails[type eq "work"].value eq "a"`, "", " ", `eq`, `userName eq "\\u00e9"`, `urn:ietf:params:scim:schemas:core:2.0:User:userName eq "a"`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		for _, resource := range []Resource{Users, Groups} {
			filter, failure := ParseFilter(raw, resource)
			if failure != nil {
				if failure.Status != 400 || failure.Type != TypeInvalidFilter {
					t.Fatalf("failure = %v", failure)
				}
				continue
			}
			if filter.IsZero() {
				continue
			}
			allowed := map[string]bool{"userName": resource == Users, "displayName": resource == Groups, "externalId": true}
			if !allowed[filter.Attribute] || filter.Value == "" {
				t.Fatalf("accepted %q as %+v", raw, filter)
			}
		}
	})
}

// FuzzNormalizePatch feeds arbitrary PATCH bodies to both normalizers: they must never panic and must answer
// either a normalized patch or a 400 error.
func FuzzNormalizePatch(f *testing.F) {
	for _, seed := range []string{
		`{"Operations":[{"op":"Replace","path":"active","value":"False"}]}`,
		`{"Operations":[{"op":"replace","value":{"active":false}}]}`,
		`{"Operations":[{"op":"remove","path":"members[value eq \"x\"]"}]}`,
		`{"Operations":[{"op":"add","path":"members","value":[{"value":"x"}]}]}`,
		`{"Operations":[{"op":"replace","path":"emails[type eq \"work\"].value","value":"a@b.c"}]}`,
		`{"Operations":[{"op":"replace","path":"urn:x:y:z.q","value":1}]}`, `{}`, `[]`, `null`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if _, failure := NormalizeUserPatch(body); failure != nil && failure.Status != 400 {
			t.Fatalf("user failure = %v", failure)
		}
		if _, failure := NormalizeGroupPatch(body); failure != nil && failure.Status != 400 {
			t.Fatalf("group failure = %v", failure)
		}
	})
}
