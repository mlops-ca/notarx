package events

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCatalogue(t *testing.T) {
	want := []string{"artifact.published", "artifact.scan.completed", "artifact.quarantined", "artifact.released", "artifact.promoted",
		"promotion.pending_approval", "promotion.approved", "promotion.rejected", "promotion.failed", "replication.completed", "replication.failed",
		"repository.visibility_changed"}
	got := Types()
	if len(got) != len(want) {
		t.Fatalf("types = %v", got)
	}
	for i, name := range want {
		if string(got[i]) != name || !got[i].Valid() {
			t.Errorf("type %d = %q, want %q", i, got[i], name)
		}
	}
	got[0] = "mutated"
	if Types()[0] != ArtifactPublished {
		t.Error("Types returns the internal slice")
	}
	for _, legacy := range []Type{"cargo.promotion.completed", "cargo.promotion.failed", "cargo.replication.completed", "cargo.replication.failed", ""} {
		if legacy.Valid() {
			t.Errorf("%q must not be a valid type", legacy)
		}
	}
}

func TestDedupeKeyVersionPerType(t *testing.T) {
	base := Event{RepositoryID: "r", Coordinate: "pkg@1", Digest: "sha256:aa"}
	scan := base
	scan.Type, scan.Version = ArtifactScanCompleted, "scan-1"
	rescan := scan
	rescan.Version = "scan-2"
	published := base
	published.Type = ArtifactPublished
	quarantined := base
	quarantined.Type, quarantined.Version = ArtifactQuarantined, "3"
	released := quarantined
	released.Type = ArtifactReleased
	promoted := base
	promoted.Type, promoted.Version = ArtifactPromoted, "promotion-9"
	if scan.DedupeKey() != "artifact.scan.completed|r|pkg@1|sha256:aa|scan-1" {
		t.Fatalf("scan key = %q", scan.DedupeKey())
	}
	if scan.DedupeKey() == rescan.DedupeKey() {
		t.Error("a re-scan must be a new event")
	}
	if published.DedupeKey() != "artifact.published|r|pkg@1|sha256:aa|" {
		t.Fatalf("published key = %q", published.DedupeKey())
	}
	if quarantined.DedupeKey() == released.DedupeKey() || promoted.DedupeKey() == quarantined.DedupeKey() {
		t.Error("types share a key")
	}
}

func TestValidateAndPrepare(t *testing.T) {
	if err := (Event{Type: "nope"}).Validate(); err == nil {
		t.Error("unknown type accepted")
	}
	for _, payload := range []string{"[]", "null", "1", "{"} {
		if err := (Event{Type: ArtifactPublished, Payload: json.RawMessage(payload)}).Validate(); err == nil {
			t.Errorf("payload %q accepted", payload)
		}
	}
	if err := (Event{Type: ArtifactPublished, Payload: json.RawMessage(`{"a":1}`)}).Validate(); err != nil {
		t.Error(err)
	}
	prepared := Event{Type: ArtifactReleased}.Prepare()
	if len(prepared.ID) != 36 || prepared.ID[14] != '7' || !prepared.OccurredAt.IsZero() || string(prepared.Payload) != "{}" {
		t.Errorf("prepared = %+v", prepared)
	}
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	again := Event{ID: "keep", Type: ArtifactReleased, OccurredAt: fixed}.Prepare()
	if again.ID != "keep" || !again.OccurredAt.Equal(fixed) || again.OccurredAt.Location() != time.UTC {
		t.Errorf("prepared = %+v", again)
	}
	if !HasInternalConsumer(ArtifactPublished) || HasInternalConsumer(ArtifactReleased) {
		t.Error("internal consumer set is wrong")
	}
}

// recorder is a database/sql driver that records the statement and arguments of the emit call and answers with a boolean.
type recorder struct {
	mu       sync.Mutex
	queries  []string
	args     [][]driver.Value
	answer   bool
	answerAt time.Time
	fail     error
	// execs and execArgs record the statements run with Exec (the rule fan-out); failExec makes them fail.
	execs    []string
	execArgs [][]driver.Value
	failExec error
}

type recorderDriver struct{ r *recorder }

func (d recorderDriver) Open(string) (driver.Conn, error) { return recorderConn(d), nil }

type recorderConn struct{ r *recorder }

func (c recorderConn) Prepare(query string) (driver.Stmt, error) {
	return recorderStmt{c.r, query}, nil
}
func (c recorderConn) Close() error { return nil }

// CheckNamedValue lets the fan-out pass its rule list as a slice, which the default converter refuses.
func (c recorderConn) CheckNamedValue(*driver.NamedValue) error { return nil }
func (c recorderConn) Begin() (driver.Tx, error)                { return nil, errors.New("unused") }

type recorderStmt struct {
	r     *recorder
	query string
}

func (s recorderStmt) Close() error  { return nil }
func (s recorderStmt) NumInput() int { return -1 }
func (s recorderStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.r.execs = append(s.r.execs, s.query)
	s.r.execArgs = append(s.r.execArgs, args)
	if s.r.failExec != nil {
		return nil, s.r.failExec
	}
	return driver.RowsAffected(1), nil
}
func (s recorderStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.r.queries = append(s.r.queries, s.query)
	s.r.args = append(s.r.args, args)
	if s.r.fail != nil {
		return nil, s.r.fail
	}
	return &recorderRows{answer: s.r.answer, at: s.r.answerAt}, nil
}

type recorderRows struct {
	answer bool
	at     time.Time
	done   bool
}

func (r *recorderRows) Columns() []string { return []string{"emit_domain_event"} }
func (r *recorderRows) Close() error      { return nil }
func (r *recorderRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	if r.answer {
		dest[0] = r.at
	} else {
		dest[0] = nil
	}
	return nil
}

var driverCount int
var driverMu sync.Mutex

func openRecorder(t *testing.T, r *recorder) *sql.DB {
	t.Helper()
	driverMu.Lock()
	driverCount++
	name := "events-recorder-" + string(rune('a'+driverCount))
	driverMu.Unlock()
	sql.Register(name, recorderDriver{r})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestEmitCallsTheDatabaseFunction(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	r := &recorder{answer: true, answerAt: at}
	db := openRecorder(t, r)
	result, err := Emit(context.Background(), db, Event{ID: "0192d9a0-0000-7000-8000-000000000001", Type: ArtifactScanCompleted, RepositoryID: "repo",
		Format: "raw", Coordinate: "a/b", Digest: "sha256:aa", Version: "scan-1", Payload: json.RawMessage(`{"k":"v"}`), OccurredAt: time.Unix(1, 0)})
	if err != nil || !result.Written || !result.OccurredAt.Equal(at) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(r.queries) != 1 || !strings.Contains(r.queries[0], "emit_domain_event(") {
		t.Fatalf("queries = %v", r.queries)
	}
	args := r.args[0]
	if len(args) != 9 {
		t.Fatalf("the database function takes no caller-supplied time, got %d args", len(args))
	}
	if args[1] != "artifact.scan.completed" || args[2] != "repo" || args[7] != "artifact.scan.completed|repo|a/b|sha256:aa|scan-1" || args[8] != false {
		t.Errorf("args = %v", args)
	}
	if string(args[6].([]byte)) != `{"k":"v"}` {
		t.Errorf("payload = %v", args[6])
	}
	// artifact.published has an internal consumer, so it is always written.
	if _, err = Emit(context.Background(), db, Event{Type: ArtifactPublished}); err != nil {
		t.Fatal(err)
	}
	if r.args[1][8] != true || string(r.args[1][6].([]byte)) != "{}" {
		t.Errorf("published args = %v", r.args[1])
	}
}

func TestEmitReportsNoRowAndErrors(t *testing.T) {
	r := &recorder{}
	db := openRecorder(t, r)
	if result, err := Emit(context.Background(), db, Event{Type: ArtifactReleased}); err != nil || result.Written || !result.OccurredAt.IsZero() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := Emit(context.Background(), db, Event{Type: "bogus"}); err == nil {
		t.Error("unknown type emitted")
	}
	if len(r.queries) != 1 {
		t.Errorf("an invalid event reached the database: %v", r.queries)
	}
	r.fail = errors.New("boom")
	if _, err := Emit(context.Background(), db, Event{Type: ArtifactReleased}); err == nil {
		t.Error("database failure swallowed")
	}
}

func TestPublishedBuildsTheOnePerPublicationEvent(t *testing.T) {
	event := Published("repo", "oci", "app", "sha256:aa", "alice")
	if event.Type != ArtifactPublished || event.Version != "" || event.DedupeKey() != "artifact.published|repo|app|sha256:aa|" {
		t.Fatalf("event = %+v", event)
	}
	var payload map[string]string
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload["format"] != "oci" || payload["actor"] != "alice" {
		t.Fatalf("payload = %s (%v)", event.Payload, err)
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	var anonymous map[string]string
	if err := json.Unmarshal(Published("repo", "raw", "a", "sha256:bb", "").Payload, &anonymous); err != nil {
		t.Fatal(err)
	}
	if _, ok := anonymous["actor"]; ok {
		t.Errorf("an unknown actor was written: %v", anonymous)
	}
}

func TestEmitOnceCallsTheGuardedFunctionWithTheFloor(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	floor := time.Date(2026, 5, 1, 0, 0, 0, 0, time.FixedZone("x", 3600))
	r := &recorder{answer: true, answerAt: at}
	db := openRecorder(t, r)
	result, err := EmitOnce(context.Background(), db, Published("repo", "raw", "a/b", "sha256:aa", ""), floor)
	if err != nil || !result.Written || !result.OccurredAt.Equal(at) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(r.queries) != 1 || !strings.Contains(r.queries[0], "emit_domain_event_once(") {
		t.Fatalf("queries = %v", r.queries)
	}
	args := r.args[0]
	if len(args) != 10 || args[7] != "artifact.published|repo|a/b|sha256:aa|" || args[8] != true {
		t.Fatalf("args = %v", args)
	}
	if got, ok := args[9].(time.Time); !ok || !got.Equal(floor) || got.Location() != time.UTC {
		t.Errorf("floor = %v", args[9])
	}
	// The database answers NULL when the key is present: nothing was written.
	r.answer = false
	if result, err = EmitOnce(context.Background(), db, Published("repo", "raw", "a/b", "sha256:aa", ""), floor); err != nil || result.Written {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err = EmitOnce(context.Background(), db, Event{Type: "bogus"}, floor); err == nil {
		t.Error("unknown type emitted")
	}
	r.fail = errors.New("boom")
	if _, err = EmitOnce(context.Background(), db, Published("repo", "raw", "a", "sha256:aa", ""), floor); err == nil {
		t.Error("database failure swallowed")
	}
}

// fakeFanout is a RuleFanout that returns fixed rules, or an error, and records the events it saw.
type fakeFanout struct {
	rules []string
	err   error
	seen  []Event
}

func (f *fakeFanout) MatchingRules(_ context.Context, _ DB, event Event) ([]string, error) {
	f.seen = append(f.seen, event)
	return f.rules, f.err
}

func installFanout(t *testing.T, f RuleFanout) {
	t.Helper()
	SetRuleFanout(f)
	t.Cleanup(func() { SetRuleFanout(nil) })
}

func TestEmitFansOutToTheMatchingRulesInTheSameTransaction(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	r := &recorder{answer: true, answerAt: at}
	db := openRecorder(t, r)
	fan := &fakeFanout{rules: []string{"rule-1", "rule-2"}}
	installFanout(t, fan)
	event := Published("repo", "raw", "a/b", "sha256:aa", "")
	event.ID = "0192d9a0-0000-7000-8000-000000000002"
	result, err := Emit(context.Background(), db, event)
	if err != nil || !result.Written {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(fan.seen) != 1 || fan.seen[0].RepositoryID != "repo" || fan.seen[0].Coordinate != "a/b" || fan.seen[0].ID != event.ID {
		t.Fatalf("fan-out saw %+v", fan.seen)
	}
	if len(r.execs) != 1 || !strings.Contains(r.execs[0], "INSERT INTO promotion_evaluations") || !strings.Contains(r.execs[0], "ON CONFLICT (event_seq,rule_id) DO NOTHING") {
		t.Fatalf("execs = %v", r.execs)
	}
	args := r.execArgs[0]
	if len(args) != 3 || args[0] != event.ID {
		t.Fatalf("exec args = %v", args)
	}
	if got, ok := args[1].(time.Time); !ok || !got.Equal(at) {
		t.Errorf("occurred_at = %v", args[1])
	}
}

func TestEmitOnceFansOutToo(t *testing.T) {
	r := &recorder{answer: true, answerAt: time.Now()}
	db := openRecorder(t, r)
	fan := &fakeFanout{rules: []string{"rule-1"}}
	installFanout(t, fan)
	if _, err := EmitOnce(context.Background(), db, Published("repo", "raw", "a", "sha256:aa", ""), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(r.execs) != 1 || len(fan.seen) != 1 {
		t.Fatalf("execs=%v seen=%v", r.execs, fan.seen)
	}
}

func TestFanOutSkipsWhatNoRuleCanWant(t *testing.T) {
	r := &recorder{answer: true, answerAt: time.Now()}
	db := openRecorder(t, r)
	fan := &fakeFanout{}
	installFanout(t, fan)
	// No matching rule: nothing is inserted.
	if _, err := Emit(context.Background(), db, Published("repo", "raw", "a", "sha256:aa", "")); err != nil || len(r.execs) != 0 {
		t.Fatalf("execs=%v err=%v", r.execs, err)
	}
	fan.rules = []string{"rule-1"}
	// A type that starts no evaluation, an event without a repository, and an event nothing wrote do not ask the fan-out.
	for _, event := range []Event{{Type: ArtifactReleased, RepositoryID: "repo"}, {Type: ArtifactPublished}} {
		fan.seen = nil
		if _, err := Emit(context.Background(), db, event); err != nil || len(fan.seen) != 0 || len(r.execs) != 0 {
			t.Fatalf("event %+v: seen=%v execs=%v err=%v", event, fan.seen, r.execs, err)
		}
	}
	r.answer = false
	fan.seen = nil
	if result, err := Emit(context.Background(), db, Published("repo", "raw", "a", "sha256:aa", "")); err != nil || result.Written || len(fan.seen) != 0 {
		t.Fatalf("result=%+v seen=%v err=%v", result, fan.seen, err)
	}
	// No fan-out installed: the event is written and nothing else happens.
	SetRuleFanout(nil)
	r.answer = true
	if result, err := Emit(context.Background(), db, Published("repo", "raw", "a", "sha256:aa", "")); err != nil || !result.Written || len(r.execs) != 0 {
		t.Fatalf("result=%+v execs=%v err=%v", result, r.execs, err)
	}
}

func TestFanOutFailureFailsTheEmission(t *testing.T) {
	r := &recorder{answer: true, answerAt: time.Now()}
	db := openRecorder(t, r)
	fan := &fakeFanout{err: errors.New("rule set unavailable")}
	installFanout(t, fan)
	if result, err := Emit(context.Background(), db, Published("repo", "raw", "a", "sha256:aa", "")); err == nil || result.Written || !strings.Contains(err.Error(), "rule set unavailable") {
		t.Fatalf("fan-out lookup failure: result=%+v err=%v", result, err)
	}
	fan.err, fan.rules = nil, []string{"rule-1"}
	r.failExec = errors.New("insert refused")
	if result, err := Emit(context.Background(), db, Published("repo", "raw", "a", "sha256:aa", "")); err == nil || result.Written || !strings.Contains(err.Error(), "insert refused") {
		t.Fatalf("fan-out insert failure: result=%+v err=%v", result, err)
	}
	if _, err := EmitOnce(context.Background(), db, Published("repo", "raw", "a", "sha256:aa", ""), time.Now()); err == nil {
		t.Fatal("EmitOnce must fail when the fan-out insert fails")
	}
}
