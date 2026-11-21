package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func reconcileEvents(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	getenv := func(key string) string { return env[key] }
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }
	code := runReconcileEvents(context.Background(), args, getenv, lookup, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestReconcileEventsRefusesBadInvocations(t *testing.T) {
	if code, _, stderr := reconcileEvents(t, nil, "extra"); code != 2 || !strings.Contains(stderr, "usage") {
		t.Fatalf("an argument: code=%d stderr=%q", code, stderr)
	}
	if code, stdout, _ := reconcileEvents(t, nil, "-h"); code != 0 || !strings.Contains(stdout, "usage") {
		t.Fatalf("help: code=%d stdout=%q", code, stdout)
	}
	if code, _, stderr := reconcileEvents(t, map[string]string{"NOTARX_DATABASE_DRIVER": "mysql"}); code != 2 || !strings.Contains(stderr, "postgres") {
		t.Fatalf("mysql: code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := reconcileEvents(t, nil); code != 1 || !strings.Contains(stderr, "NOTARX_DATABASE_URL") {
		t.Fatalf("no database: code=%d stderr=%q", code, stderr)
	}
	env := map[string]string{"NOTARX_DATABASE_URL": "postgres://x", "NOTARX_EVENTS_RETENTION": "soon"}
	if code, _, stderr := reconcileEvents(t, env); code != 2 || stderr == "" {
		t.Fatalf("bad retention: code=%d stderr=%q", code, stderr)
	}
}

func TestReconcileEventsReportsAnUnreachableDatabase(t *testing.T) {
	env := map[string]string{"NOTARX_DATABASE_URL": "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1", "NOTARX_EVENTS_RETENTION": "off"}
	code, stdout, stderr := reconcileEvents(t, env)
	if code != 1 || !strings.Contains(stdout, "published events written: 0") || !strings.Contains(stderr, "reconcile-events:") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestReconcileEventsEvaluatesRulesUnlessPromotionIsDisabled(t *testing.T) {
	for value, want := range map[string]bool{"": true, "true": true, " true ": true, "false": false, " false\n": false} {
		if got := promotionEnabled(func(string) string { return value }); got != want {
			t.Errorf("NOTARX_PROMOTION_ENABLED=%q: enabled = %v, want %v", value, got, want)
		}
	}
}
