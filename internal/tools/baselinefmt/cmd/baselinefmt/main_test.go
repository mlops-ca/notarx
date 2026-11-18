package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunNormalisesStdin(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("SET x = 1;\nCREATE TABLE public.t (id int);\n")
	if err := run(in, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "-- +goose Up\n") || !strings.Contains(out.String(), "CREATE TABLE public.t") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
}

func TestRunReportsRejectedInput(t *testing.T) {
	if err := run(strings.NewReader("INSERT INTO t VALUES (1);\n"), &bytes.Buffer{}); err == nil {
		t.Fatal("expected an error")
	}
}
