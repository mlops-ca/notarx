package scim

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseFilter(t *testing.T) {
	valid := []struct {
		raw      string
		resource Resource
		want     Filter
	}{
		{"", Users, Filter{}},
		{"   ", Users, Filter{}},
		{`userName eq "alice@example.com"`, Users, Filter{"userName", "alice@example.com"}},
		{`USERNAME EQ "Alice"`, Users, Filter{"userName", "Alice"}},
		{`  externalId   eq   "abc-123"  `, Users, Filter{"externalId", "abc-123"}},
		{`urn:ietf:params:scim:schemas:core:2.0:User:userName eq "a"`, Users, Filter{"userName", "a"}},
		{`displayName eq "Platform \"Core\""`, Groups, Filter{"displayName", `Platform "Core"`}},
		{`externalId eq "g-1"`, Groups, Filter{"externalId", "g-1"}},
	}
	for _, test := range valid {
		got, failure := ParseFilter(test.raw, test.resource)
		if failure != nil || got != test.want {
			t.Errorf("ParseFilter(%q) = %+v, %v; want %+v", test.raw, got, failure, test.want)
		}
	}
	invalid := []struct {
		raw      string
		resource Resource
	}{
		{`userName ne "a"`, Users},
		{`userName co "a"`, Users},
		{`userName sw "a"`, Users},
		{`userName pr`, Users},
		{`userName eq "a" and externalId eq "b"`, Users},
		{`userName eq "a" or userName eq "b"`, Users},
		{`not (userName eq "a")`, Users},
		{`emails[type eq "work"].value eq "a"`, Users},
		{`displayName eq "a"`, Users},
		{`userName eq "a"`, Groups},
		{`active eq true`, Users},
		{`userName eq a`, Users},
		{`userName eq ""`, Users},
		{`userName eq "unterminated`, Users},
		{`userName eq "a" trailing`, Users},
		{`userName`, Users},
		{`eq`, Users},
		{strings.Repeat("a", MaxFilterLength+1), Users},
		{"userName eq \"\xff\"", Users},
	}
	for _, test := range invalid {
		_, failure := ParseFilter(test.raw, test.resource)
		if failure == nil || failure.Type != TypeInvalidFilter || failure.Status != 400 {
			t.Errorf("ParseFilter(%q) = %v, want 400 invalidFilter", test.raw, failure)
		}
	}
}

func str(value string) *string { return &value }

func TestNormalizeUserPatchEntraShapes(t *testing.T) {
	// Entra ID without aadOptscim062020: capitalized ops and string booleans.
	off, failure := NormalizeUserPatch([]byte(`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[
		{"op":"Replace","path":"active","value":"False"},
		{"op":"Replace","path":"emails[type eq \"work\"].value","value":"a@example.com"},
		{"op":"Add","path":"name.givenName","value":"Ada"},
		{"op":"Replace","path":"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:manager","value":"m"},
		{"op":"Replace","path":"displayName","value":"Ada L"}]}`))
	if failure != nil || off.Active == nil || *off.Active || stringOrNil(off.Email) != "a@example.com" || stringOrNil(off.DisplayName) != "Ada L" {
		t.Fatalf("flag off = %+v, %v", off, failure)
	}
	if len(off.Ignored) != 2 || off.Ignored[0] != "name.givenname" {
		t.Fatalf("ignored = %v", off.Ignored)
	}
	// With the flag: booleans, dotted pathless keys and an unquoted selector value.
	on, failure := NormalizeUserPatch([]byte(`{"Operations":[
		{"op":"replace","path":"active","value":true},
		{"op":"replace","value":{"userName":"ada","name.familyName":"L","emails[type eq work].value":"b@example.com"}}]}`))
	if failure != nil || on.Active == nil || !*on.Active || stringOrNil(on.UserName) != "ada" || stringOrNil(on.Email) != "b@example.com" {
		t.Fatalf("flag on = %+v, %v", on, failure)
	}
}

func stringOrNil(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

func TestNormalizeUserPatchOktaShapes(t *testing.T) {
	okta, failure := NormalizeUserPatch([]byte(`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","value":{"active":false}}]}`))
	if failure != nil || okta.Active == nil || *okta.Active {
		t.Fatalf("okta deactivate = %+v, %v", okta, failure)
	}
	nested, failure := NormalizeUserPatch([]byte(`{"Operations":[{"op":"replace","value":{"displayName":"Grace","name":{"formatted":"Grace H","givenName":"Grace"},"title":"Admiral"}}]}`))
	if failure != nil || stringOrNil(nested.DisplayName) != "Grace H" || len(nested.Ignored) != 2 {
		t.Fatalf("okta profile = %+v, %v", nested, failure)
	}
	emails, failure := NormalizeUserPatch([]byte(`{"Operations":[{"op":"replace","path":"emails","value":[{"value":"home@example.com","type":"home"},{"value":"work@example.com","type":"work","primary":true}]}]}`))
	if failure != nil || stringOrNil(emails.Email) != "work@example.com" {
		t.Fatalf("emails = %+v, %v", emails, failure)
	}
}

func TestNormalizeUserPatchRejections(t *testing.T) {
	for name, test := range map[string]struct {
		body string
		want Type
	}{
		"remove userName":       {`{"Operations":[{"op":"remove","path":"userName"}]}`, TypeInvalidPath},
		"remove active":         {`{"Operations":[{"op":"remove","path":"active"}]}`, TypeInvalidPath},
		"replace id":            {`{"Operations":[{"op":"replace","path":"id","value":"x"}]}`, TypeInvalidPath},
		"selector on active":    {`{"Operations":[{"op":"replace","path":"active[type eq \"x\"]","value":true}]}`, TypeInvalidPath},
		"sub attribute":         {`{"Operations":[{"op":"replace","path":"userName.x","value":"x"}]}`, TypeInvalidPath},
		"broken core path":      {`{"Operations":[{"op":"replace","path":"externalId[","value":"x"}]}`, TypeInvalidPath},
		"unknown op":            {`{"Operations":[{"op":"move","path":"active","value":true}]}`, TypeInvalidSyntax},
		"no operations":         {`{"Operations":[]}`, TypeInvalidSyntax},
		"not json":              {`nope`, TypeInvalidSyntax},
		"remove without path":   {`{"Operations":[{"op":"remove"}]}`, TypeNoTarget},
		"active not a boolean":  {`{"Operations":[{"op":"replace","path":"active","value":"maybe"}]}`, TypeInvalidValue},
		"userName empty":        {`{"Operations":[{"op":"replace","path":"userName","value":" "}]}`, TypeInvalidValue},
		"pathless needs object": {`{"Operations":[{"op":"replace","value":"x"}]}`, TypeInvalidValue},
		"too many operations":   {`{"Operations":[` + strings.TrimSuffix(strings.Repeat(`{"op":"replace","path":"active","value":true},`, MaxPatchOperations+1), ",") + `]}`, TypeTooMany},
	} {
		_, failure := NormalizeUserPatch([]byte(test.body))
		if failure == nil || failure.Type != test.want || failure.Status != 400 {
			t.Errorf("%s: failure = %v, want 400 %s", name, failure, test.want)
		}
	}
	// A malformed path of an attribute the server does not store is not an error: Entra ID treats 400 as a failure.
	patch, failure := NormalizeUserPatch([]byte(`{"Operations":[{"op":"replace","path":"phoneNumbers[type eq","value":"1"}]}`))
	if failure != nil || len(patch.Ignored) != 1 {
		t.Fatalf("malformed non-core path = %+v, %v", patch, failure)
	}
}

func TestNormalizeGroupPatch(t *testing.T) {
	patch, failure := NormalizeGroupPatch([]byte(`{"Operations":[
		{"op":"Add","path":"members","value":[{"value":"u1","display":"one"},{"value":"u2"}]},
		{"op":"Remove","path":"members[value eq \"u3\"]"},
		{"op":"remove","path":"members","value":[{"value":"u4"}]},
		{"op":"replace","value":{"displayName":"New name","externalId":"g-9"}}]}`))
	if failure != nil || strings.Join(patch.AddMembers, ",") != "u1,u2" || strings.Join(patch.RemoveMembers, ",") != "u3,u4" ||
		stringOrNil(patch.DisplayName) != "New name" || stringOrNil(patch.ExternalID) != "g-9" || patch.ReplaceMembers != nil {
		t.Fatalf("patch = %+v, %v", patch, failure)
	}
	replaced, failure := NormalizeGroupPatch([]byte(`{"Operations":[{"op":"replace","path":"members","value":[{"value":"u1"}]}]}`))
	if failure != nil || replaced.ReplaceMembers == nil || len(*replaced.ReplaceMembers) != 1 {
		t.Fatalf("replace members = %+v, %v", replaced, failure)
	}
	cleared, failure := NormalizeGroupPatch([]byte(`{"Operations":[{"op":"remove","path":"members"}]}`))
	if failure != nil || cleared.ReplaceMembers == nil || len(*cleared.ReplaceMembers) != 0 {
		t.Fatalf("remove all members = %+v, %v", cleared, failure)
	}
	ignored, failure := NormalizeGroupPatch([]byte(`{"Operations":[{"op":"replace","path":"description","value":"x"}]}`))
	if failure != nil || len(ignored.Ignored) != 1 {
		t.Fatalf("ignored = %+v, %v", ignored, failure)
	}
	for _, body := range []string{
		`{"Operations":[{"op":"replace","path":"id","value":"x"}]}`,
		`{"Operations":[{"op":"remove","path":"displayName"}]}`,
		`{"Operations":[{"op":"add","path":"members"}]}`,
		`{"Operations":[{"op":"add","path":"members","value":"nope"}]}`,
	} {
		if _, failure := NormalizeGroupPatch([]byte(body)); failure == nil || failure.Status != 400 {
			t.Errorf("NormalizeGroupPatch(%s) = %v, want 400", body, failure)
		}
	}
}

func TestParseUserAndGroup(t *testing.T) {
	user, failure := ParseUser([]byte(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":" ada ","externalId":"e-1","name":{"givenName":"Ada","familyName":"Lovelace"},
		"emails":[{"value":"ada@example.com","primary":true,"type":"work"}],"active":"True"}`))
	if failure != nil || user.UserName != "ada" || *user.ExternalID != "e-1" || user.DisplayName != "Ada Lovelace" || user.Email != "ada@example.com" || user.Active == nil || !*user.Active {
		t.Fatalf("user = %+v, %v", user, failure)
	}
	for _, body := range []string{`{}`, `{"userName":"a","active":"maybe"}`, `{"userName":"a","externalId":5}`, `{"userName":"a","emails":"x"}`, `nope`} {
		if _, failure := ParseUser([]byte(body)); failure == nil {
			t.Errorf("ParseUser(%s) was accepted", body)
		}
	}
	group, failure := ParseGroup([]byte(`{"displayName":"Eng","externalId":"g","members":[{"value":"u1"}]}`))
	if failure != nil || group.DisplayName != "Eng" || group.ExternalID != "g" || len(group.MemberIDs) != 1 || !group.HasMembers {
		t.Fatalf("group = %+v, %v", group, failure)
	}
	if bare, _ := ParseGroup([]byte(`{"displayName":"Eng"}`)); bare.HasMembers {
		t.Fatal("an absent members attribute was reported as present")
	}
	for _, body := range []string{`{}`, `{"displayName":"a","members":"x"}`, `{"displayName":"a","externalId":1}`, `nope`} {
		if _, failure := ParseGroup([]byte(body)); failure == nil {
			t.Errorf("ParseGroup(%s) was accepted", body)
		}
	}
}

func TestResourceDocuments(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	meta := Meta{ResourceType: "User", Created: now, LastModified: now, Location: "https://x/scim/v2/Users/1", Version: ETag("3")}
	encoded, err := json.Marshal(UserDocument{ID: "1", ExternalID: "e", UserName: "ada", DisplayName: "Ada", Email: "a@x.io", Active: true, Meta: meta})
	if err != nil {
		t.Fatal(err)
	}
	var user map[string]any
	_ = json.Unmarshal(encoded, &user)
	if user["userName"] != "ada" || user["active"] != true || user["externalId"] != "e" || user["name"].(map[string]any)["formatted"] != "Ada" ||
		user["emails"].([]any)[0].(map[string]any)["primary"] != true || user["meta"].(map[string]any)["version"] != `W/"3"` {
		t.Fatalf("user document = %s", encoded)
	}
	bare, _ := json.Marshal(UserDocument{ID: "1", UserName: "ada", Meta: meta})
	if strings.Contains(string(bare), "externalId") || strings.Contains(string(bare), "emails") || strings.Contains(string(bare), "displayName") {
		t.Fatalf("empty attributes were rendered: %s", bare)
	}
	withMembers, _ := json.Marshal(GroupDocument{ID: "g", DisplayName: "Eng", WithMembers: true, Meta: meta})
	if !strings.Contains(string(withMembers), `"members":[]`) {
		t.Fatalf("a group with no members must render an empty members array: %s", withMembers)
	}
	without, _ := json.Marshal(GroupDocument{ID: "g", DisplayName: "Eng", Meta: meta})
	if strings.Contains(string(without), "members") {
		t.Fatalf("excluded members were rendered: %s", without)
	}
	list, _ := json.Marshal(ListResponse{TotalResults: 1, StartIndex: 1, NextCursor: "c"})
	if !strings.Contains(string(list), `"Resources":[]`) || !strings.Contains(string(list), `"nextCursor":"c"`) {
		t.Fatalf("list = %s", list)
	}
	failure := NewError(409, TypeUniqueness, "exists")
	body, _ := json.Marshal(failure)
	if !strings.Contains(string(body), `"status":"409"`) || !strings.Contains(string(body), `"scimType":"uniqueness"`) || !errors.Is(failure, failure) {
		t.Fatalf("error = %s", body)
	}
}

func TestDiscoveryMatchesAdvertisedCapabilities(t *testing.T) {
	config := ServiceProviderConfig(Capabilities{MaxResults: 200}, "https://x/scim/v2", time.Now())
	flag := func(name string) bool { return config[name].(map[string]any)["supported"].(bool) }
	if !flag("patch") || flag("bulk") || !flag("filter") || flag("changePassword") || flag("sort") || !flag("etag") {
		t.Fatalf("capabilities = %v", config)
	}
	if config["filter"].(map[string]any)["maxResults"] != 200 || config["excludedAttributes"].(map[string]any)["attributes"].([]string)[0] != "members" {
		t.Fatalf("limits = %v", config)
	}
	if got := len(ResourceTypes("https://x/scim/v2")); got != 2 {
		t.Fatalf("resource types = %d", got)
	}
	schemas := Schemas("https://x/scim/v2")
	if len(schemas) != 2 || schemas[0]["id"] != SchemaUser || schemas[1]["id"] != SchemaGroup {
		t.Fatalf("schemas = %v", schemas)
	}
}
