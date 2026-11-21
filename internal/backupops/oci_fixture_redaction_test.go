package backupops

import (
	"errors"
	"strings"
	"testing"
)

func TestRedactedDockerOutputKeepsTheCauseAndDropsCredentials(t *testing.T) {
	out := redactedDockerOutput([]byte("Error response from daemon: toomanyrequests: rate limit pulling https://user:hunter2@registry.example/v2/ with Authorization: Bearer abc.def password=hunter3 token: xyz"), errors.New("exit status 1"))
	for _, secret := range []string{"hunter2", "hunter3", "abc.def", "xyz"} {
		if strings.Contains(out, secret) {
			t.Fatalf("%q leaked in %q", secret, out)
		}
	}
	if !strings.Contains(out, "toomanyrequests: rate limit") || !strings.Contains(out, "https://user:[redacted]@registry.example") {
		t.Fatalf("the cause was lost: %q", out)
	}
	if got := redactedDockerOutput(nil, errors.New("exit status 125")); got != "exit status 125" {
		t.Fatalf("empty output = %q", got)
	}
	if got := redactedDockerOutput([]byte(strings.Repeat("x", 5000)), errors.New("e")); len(got) != 2048+3 {
		t.Fatalf("long output cut to %d", len(got))
	}
}
