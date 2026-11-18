package repository

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBuildAuditPageQueryUsesOnlySuppliedFilters(t *testing.T) {
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	sql, args, limit := buildAuditPageQuery(AuditQuery{Repository: "r", Limit: 10}, now)
	if limit != 10 {
		t.Fatalf("limit = %d", limit)
	}
	for _, absent := range []string{"group_name =", "outcome =", "actor =", "operation =", "format =", "$1 = ''", "IS NULL"} {
		if strings.Contains(sql, absent) {
			t.Fatalf("unsupplied filter %q leaked into the query:\n%s", absent, sql)
		}
	}
	for _, present := range []string{"repository = $1", "security_audit_log", "resolver_audit_log", "UNION ALL", "ORDER BY occurred_at DESC, id DESC, class_rank DESC"} {
		if !strings.Contains(sql, present) {
			t.Fatalf("query lacks %q:\n%s", present, sql)
		}
	}
	// Repository, limit and one default window per table.
	if len(args) != 4 || args[0] != "r" || args[1] != 11 {
		t.Fatalf("args = %v", args)
	}
	if !args[2].(time.Time).Equal(now.Add(-730*24*time.Hour)) || !args[3].(time.Time).Equal(now.Add(-90*24*time.Hour)) {
		t.Fatalf("default windows = %v %v", args[2], args[3])
	}
}

func TestBuildAuditPageQuerySingleClassAndKeyset(t *testing.T) {
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	from := now.Add(-time.Hour)
	at := now.Add(-time.Minute)
	sql, args, _ := buildAuditPageQuery(AuditQuery{
		Class: AuditClassAsync, From: from, To: now, Limit: 0, Actor: "a",
		Before: AuditCursor{OccurredAt: at, ID: 7, Class: AuditClassSync},
	}, now)
	if strings.Contains(sql, "security_audit_log") || strings.Contains(sql, "UNION") {
		t.Fatalf("a class filter must read one table:\n%s", sql)
	}
	// The async branch ranks below a sync cursor, so an equal (occurred_at, id) is still to come.
	if !strings.Contains(sql, "(occurred_at, id) <= (") {
		t.Fatalf("lower-ranked branch must include the cursor key:\n%s", sql)
	}
	if !strings.Contains(sql, "occurred_at >= $") || !strings.Contains(sql, "occurred_at <= $") {
		t.Fatalf("explicit bounds missing:\n%s", sql)
	}
	if len(args) != 6 {
		t.Fatalf("args = %v", args)
	}
	sql, _, _ = buildAuditPageQuery(AuditQuery{Class: AuditClassSync, Before: AuditCursor{OccurredAt: at, ID: 7, Class: AuditClassSync}}, now)
	if !strings.Contains(sql, "(occurred_at, id) < (") {
		t.Fatalf("equal-ranked branch must exclude the cursor key:\n%s", sql)
	}
}

func TestMemoryAuditPageMergesClassesAndFilters(t *testing.T) {
	store := NewMemoryStore()
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for i, class := range []AuditClass{AuditClassAsync, AuditClassSync, "", AuditClassSync} {
		if err := store.RecordAudit(context.Background(), AuditRecord{ID: int64(1), Class: class, Repository: "r", Outcome: AuditResolved, Actor: "a", OccurredAt: at.Add(time.Duration(i/2) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.ListAuditPage(context.Background(), AuditQuery{Limit: 10})
	if err != nil || len(all.Items) != 4 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	for _, item := range all.Items {
		if !item.Class.Valid() {
			t.Fatalf("every returned record carries its class: %+v", item)
		}
	}
	for class, want := range map[AuditClass]int{AuditClassSync: 2, AuditClassAsync: 2} {
		page, err := store.ListAuditPage(context.Background(), AuditQuery{Class: class, Limit: 10})
		if err != nil || len(page.Items) != want {
			t.Fatalf("class %s = %d items, %v", class, len(page.Items), err)
		}
	}
	// Records share occurred_at and id across classes; paging one by one must still
	// visit each exactly once, newest first, sync before async on a tie.
	seen := map[string]bool{}
	var cursor AuditCursor
	var order []string
	for i := 0; i < 5; i++ {
		page, err := store.ListAuditPage(context.Background(), AuditQuery{Limit: 1, Before: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			key := string(item.Class) + item.OccurredAt.String()
			order = append(order, key)
			if seen[key] {
				t.Fatalf("duplicate %s", key)
			}
			seen[key] = true
		}
		if page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	if len(order) != 4 {
		t.Fatalf("paged %d records, want 4: %v", len(order), order)
	}
}

func TestAuditClassHelpers(t *testing.T) {
	if !AuditClassSync.Valid() || !AuditClassAsync.Valid() || AuditClass("x").Valid() || AuditClass("").Valid() {
		t.Fatal("Valid")
	}
	if AuditClass("").effective() != AuditClassAsync || AuditClassSync.effective() != AuditClassSync {
		t.Fatal("effective")
	}
	if AuditClassSync.rank() <= AuditClassAsync.rank() || AuditClass("").rank() != AuditClassAsync.rank() {
		t.Fatal("rank")
	}
	if AuditTable(AuditClassSync) != "security_audit_log" || AuditTable("") != "resolver_audit_log" || AuditTable(AuditClassAsync) != "resolver_audit_log" {
		t.Fatal("auditTable")
	}
}
