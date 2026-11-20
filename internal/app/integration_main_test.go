//go:build integration

package app

import (
	"fmt"
	"os"
	"testing"

	"github.com/mlops-ca/notarx/internal/testsupport"
)

func TestMain(m *testing.M) {
	if err := testsupport.ValidateIsolatedEnvironment(os.Getenv); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(m.Run())
}
