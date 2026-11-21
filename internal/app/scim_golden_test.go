package app

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The golden provisioning transcripts replay the request shapes Microsoft Entra ID (with and without its
// aadOptscim062020 flag) and Okta send against the server and compare the answers with the committed expectations.
// An expected body is a subset: an object matches when every listed key matches, an array when every listed element
// matches the element at its index, "<any>" matches any present value, null means the key must be absent, and
// "${name}" is the value captured by an earlier step.

type goldenTranscript struct {
	Description string       `json:"description"`
	Steps       []goldenStep `json:"steps"`
}

type goldenStep struct {
	Name    string `json:"name"`
	Request struct {
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Body   json.RawMessage `json:"body"`
	} `json:"request"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	} `json:"response"`
	Capture map[string]string `json:"capture"`
}

// encodeGoldenPath percent-encodes the query of a transcript path, which is written the way the IdP's filter reads.
func encodeGoldenPath(path string) string {
	base, query, found := strings.Cut(path, "?")
	if !found {
		return path
	}
	values := url.Values{}
	for _, pair := range strings.Split(query, "&") {
		key, value, _ := strings.Cut(pair, "=")
		values.Add(key, value)
	}
	return base + "?" + values.Encode()
}

var goldenPlaceholder = regexp.MustCompile(`\$\{([a-z]+)\}`)

func substituteGolden(text string, captured map[string]string) string {
	return goldenPlaceholder.ReplaceAllStringFunc(text, func(match string) string {
		return captured[goldenPlaceholder.FindStringSubmatch(match)[1]]
	})
}

func matchGolden(path string, want, got any, captured map[string]string) error {
	switch expected := want.(type) {
	case nil:
		if got != nil {
			return fmt.Errorf("%s: want the key absent, got %v", path, got)
		}
	case string:
		switch {
		case expected == "<any>":
			if got == nil {
				return fmt.Errorf("%s: want a value, got none", path)
			}
		case got != substituteGolden(expected, captured):
			return fmt.Errorf("%s: want %q, got %v", path, substituteGolden(expected, captured), got)
		}
	case map[string]any:
		object, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want an object, got %v", path, got)
		}
		for key, value := range expected {
			if err := matchGolden(path+"."+key, value, object[key], captured); err != nil {
				return err
			}
		}
	case []any:
		list, ok := got.([]any)
		if !ok || len(list) < len(expected) {
			return fmt.Errorf("%s: want at least %d elements, got %v", path, len(expected), got)
		}
		if len(expected) == 0 && len(list) != 0 {
			return fmt.Errorf("%s: want an empty list, got %v", path, got)
		}
		for index, value := range expected {
			if err := matchGolden(fmt.Sprintf("%s[%d]", path, index), value, list[index], captured); err != nil {
				return err
			}
		}
	default:
		if got != want {
			return fmt.Errorf("%s: want %v, got %v", path, want, got)
		}
	}
	return nil
}

func TestSCIMGoldenProvisioningTranscripts(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "scim", "*.json"))
	if err != nil || len(files) != 3 {
		t.Fatalf("transcripts = %v, %v; want entra-flag-off, entra-flag-on and okta", files, err)
	}
	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var transcript goldenTranscript
			if err := json.Unmarshal(raw, &transcript); err != nil || transcript.Description == "" || len(transcript.Steps) == 0 {
				t.Fatalf("transcript = %v", err)
			}
			h := newSCIMHarness(t, SCIMOptions{Enabled: true})
			captured := map[string]string{}
			for _, step := range transcript.Steps {
				body := ""
				if len(step.Request.Body) > 0 {
					body = substituteGolden(string(step.Request.Body), captured)
				}
				response := h.scim(step.Request.Method, encodeGoldenPath(substituteGolden(step.Request.Path, captured)), body)
				if response.Code != step.Response.Status {
					t.Fatalf("%s: status = %d, want %d: %s", step.Name, response.Code, step.Response.Status, response.Body.String())
				}
				for name, want := range step.Response.Headers {
					if got := response.Header().Get(name); got != want {
						t.Fatalf("%s: header %s = %q, want %q", step.Name, name, got, want)
					}
				}
				if len(step.Response.Body) == 0 {
					continue
				}
				var want, got any
				if err := json.Unmarshal(step.Response.Body, &want); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatalf("%s: the response is not JSON: %s", step.Name, response.Body.String())
				}
				if err := matchGolden("body", want, got, captured); err != nil {
					t.Fatalf("%s: %v\nresponse: %s", step.Name, err, response.Body.String())
				}
				for name, field := range step.Capture {
					value, _ := got.(map[string]any)[field].(string)
					captured[name] = value
				}
			}
		})
	}
}
