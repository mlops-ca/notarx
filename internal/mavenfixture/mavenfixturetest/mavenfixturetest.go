// Package mavenfixturetest starts the recorded Maven fixture for a Go test.
package mavenfixturetest

import (
	"net/http/httptest"
	"testing"

	"github.com/mlops-ca/notarx/internal/mavenfixture"
)

// Start serves the recorded Maven Central and Gradle plugin portal responses on
// a loopback server for the life of the test. A path that was not recorded
// answers 404 and the test log names the re-record procedure (docs/ci.md).
func Start(t testing.TB) *httptest.Server {
	t.Helper()
	handler := mavenfixture.New(mavenfixture.DefaultDir(), mavenfixture.RecordMode(), t.Logf)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}
