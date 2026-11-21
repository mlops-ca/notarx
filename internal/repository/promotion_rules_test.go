package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

// promotionRuleTestStore is what the shared rule store exercise needs: the rule port, the ports of the rows a rule
// references, and the audit read.
type promotionRuleTestStore interface {
	PromotionRuleStore
	RemoteInstanceStore
	CreateHostedRepository(context.Context, HostedRepository) (HostedRepository, error)
	CreateServiceAccount(context.Context, ServiceAccount, ...AuditRecord) (ServiceAccount, error)
	ListAudits(context.Context, AuditQuery) ([]AuditRecord, error)
}

func TestMemoryPromotionRuleStore(t *testing.T) {
	exercisePromotionRuleStore(t, NewMemoryStore())
}

func exercisePromotionRuleStore(t *testing.T, store promotionRuleTestStore) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()
	repo := func(name string) HostedRepository {
		created, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: name + "-" + suffix, Format: FormatRaw})
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	source, target, other := repo("rules-source"), repo("rules-target"), repo("rules-other")
	account, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: uuid.NewString(), Name: "rules-runner-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := store.CreateRemoteInstance(ctx, RemoteInstance{ID: uuid.NewString(), Name: "rules-peer-" + suffix, BaseURL: "https://peer.example.test", InstanceID: uuid.NewString(),
		AuthKind: RemoteInstanceAuthStaticToken, TokenSealed: "v1:kid:sealed", State: RemoteInstanceStateActive})
	if err != nil {
		t.Fatal(err)
	}
	match := json.RawMessage(`{"coordinate":{"glob":"releases/**"}}`)
	local := func(name string) PromotionRule {
		return PromotionRule{ID: uuid.NewString(), Name: name + "-" + suffix, SourceRepositoryID: source.ID, TargetKind: PromotionTargetLocal, TargetRepositoryID: target.ID,
			Match: match, Conditions: json.RawMessage(`{"minAge":"72h"}`), RunAsServiceAccountID: account.ID, Enabled: true, CreatedBy: "alice"}
	}
	audit := func(operation string) AuditRecord {
		return AuditRecord{Actor: "alice", Repository: source.Name, Operation: operation, Resource: "promotion-rules/" + suffix, Format: "management"}
	}

	first, err := store.CreatePromotionRule(ctx, local("first"), 3, audit("promotion_rule.create"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != "1" || first.UpdatedBy != "alice" || first.CreatedBy != "alice" || first.CreatedAt.IsZero() || !first.Enabled || first.TargetRepositoryID != target.ID || first.RemoteInstanceID != "" {
		t.Fatalf("created = %#v", first)
	}
	if first.Approval != nil || first.Schedule != nil {
		t.Fatalf("approval and schedule are unset: %#v", first)
	}
	if got, getErr := store.GetPromotionRule(ctx, first.ID); getErr != nil || got.Name != first.Name || !jsonEqual(got.Match, match) || !jsonEqual(got.Conditions, json.RawMessage(`{"minAge":"72h"}`)) {
		t.Fatalf("get = %#v, %v", got, getErr)
	}
	if _, getErr := store.GetPromotionRule(ctx, uuid.NewString()); !errors.Is(getErr, ErrNotFound) {
		t.Fatalf("unknown rule = %v", getErr)
	}

	// A name is unique; a remote target needs an instance and a repository name; references must exist.
	if _, err = store.CreatePromotionRule(ctx, local("first"), 3); !errors.Is(err, ErrPromotionRuleNameExists) {
		t.Fatalf("duplicate name = %v", err)
	}
	remoteRule := PromotionRule{ID: uuid.NewString(), Name: "remote-" + suffix, SourceRepositoryID: source.ID, TargetKind: PromotionTargetRemote, RemoteInstanceID: remote.ID, RemoteRepository: "releases",
		Match: match, Conditions: json.RawMessage(`{}`), RunAsServiceAccountID: account.ID, Enabled: false, CreatedBy: "bob"}
	createdRemote, err := store.CreatePromotionRule(ctx, remoteRule, 3)
	if err != nil {
		t.Fatal(err)
	}
	if createdRemote.TargetKind != PromotionTargetRemote || createdRemote.TargetRepositoryID != "" || createdRemote.RemoteInstanceID != remote.ID || createdRemote.RemoteRepository != "releases" || createdRemote.Enabled {
		t.Fatalf("remote rule = %#v", createdRemote)
	}
	// A remote instance that a rule still targets cannot be deleted, and the refusal names the rule.
	var inUse *RemoteInstanceInUseError
	if err = store.DeleteRemoteInstance(ctx, remote.ID); !errors.Is(err, ErrRemoteInstanceInUse) || !errors.As(err, &inUse) ||
		len(inUse.Rules) != 1 || inUse.Rules[0].ID != createdRemote.ID || inUse.Rules[0].Name != remoteRule.Name {
		t.Fatalf("delete of a targeted remote instance = %v (%#v)", err, inUse)
	}
	for name, mutate := range map[string]func(*PromotionRule){
		"unknown source":          func(r *PromotionRule) { r.SourceRepositoryID = uuid.NewString() },
		"unknown target":          func(r *PromotionRule) { r.TargetRepositoryID = uuid.NewString() },
		"unknown service account": func(r *PromotionRule) { r.RunAsServiceAccountID = uuid.NewString() },
		"unknown remote": func(r *PromotionRule) {
			r.TargetKind, r.TargetRepositoryID, r.RemoteInstanceID, r.RemoteRepository = PromotionTargetRemote, "", uuid.NewString(), "x"
		},
	} {
		bad := local("bad-" + name)
		mutate(&bad)
		if _, err = store.CreatePromotionRule(ctx, bad, 10); !errors.Is(err, ErrPromotionRuleReference) {
			t.Fatalf("%s = %v", name, err)
		}
	}

	// The cap counts the rules of the source repository only.
	if _, err = store.CreatePromotionRule(ctx, local("third"), 3); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreatePromotionRule(ctx, local("fourth"), 3); !errors.Is(err, ErrPromotionRuleLimit) {
		t.Fatalf("over the cap = %v", err)
	}
	elsewhere := local("elsewhere")
	elsewhere.SourceRepositoryID, elsewhere.TargetRepositoryID = other.ID, target.ID
	if _, err = store.CreatePromotionRule(ctx, elsewhere, 1); err != nil {
		t.Fatalf("another source has its own cap: %v", err)
	}

	// Every edit increments the version and records the editor; a stale version is refused; the source never changes.
	edited := first
	edited.Name, edited.Enabled, edited.UpdatedBy = "renamed-"+suffix, false, "carol"
	edited.SourceRepositoryID, edited.CreatedBy = other.ID, "mallory"
	edited.Conditions = json.RawMessage(`{"securityPolicy":"pass"}`)
	updated, err := store.UpdatePromotionRule(ctx, edited, "1", audit("promotion_rule.update"))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != "2" || updated.UpdatedBy != "carol" || updated.CreatedBy != "alice" || updated.SourceRepositoryID != source.ID || updated.Enabled || updated.Name != edited.Name || !jsonEqual(updated.Conditions, edited.Conditions) {
		t.Fatalf("updated = %#v", updated)
	}
	for expected := 2; expected <= 4; expected++ {
		next, updateErr := store.UpdatePromotionRule(ctx, updated, strconv.Itoa(expected))
		if updateErr != nil || next.Version != strconv.Itoa(expected+1) {
			t.Fatalf("update at %d = %#v, %v", expected, next, updateErr)
		}
		updated = next
	}
	if _, err = store.UpdatePromotionRule(ctx, updated, "1"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version = %v", err)
	}
	clash := updated
	clash.Name = createdRemote.Name
	if _, err = store.UpdatePromotionRule(ctx, clash, updated.Version); !errors.Is(err, ErrPromotionRuleNameExists) {
		t.Fatalf("rename onto another rule = %v", err)
	}
	missing := updated
	missing.ID = uuid.NewString()
	if _, err = store.UpdatePromotionRule(ctx, missing, "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of an unknown rule = %v", err)
	}
	dangling := updated
	dangling.RunAsServiceAccountID = uuid.NewString()
	if _, err = store.UpdatePromotionRule(ctx, dangling, updated.Version); !errors.Is(err, ErrPromotionRuleReference) {
		t.Fatalf("update with an unknown service account = %v", err)
	}

	// Paging is by name; a source restriction narrows it; an empty restriction matches nothing.
	all, err := store.PagePromotionRules(ctx, PromotionRuleQuery{SourceRepositoryIDs: []string{source.ID}, Limit: 2})
	if err != nil || len(all.Items) != 2 || all.NextPageToken == "" {
		t.Fatalf("first page = %#v, %v", all, err)
	}
	rest, err := store.PagePromotionRules(ctx, PromotionRuleQuery{SourceRepositoryIDs: []string{source.ID}, Limit: 2, PageToken: all.NextPageToken})
	if err != nil || len(rest.Items) != 1 || rest.NextPageToken != "" || rest.Items[0].Name <= all.Items[1].Name || all.Items[0].Name >= all.Items[1].Name {
		t.Fatalf("second page = %#v, %v", rest, err)
	}
	if _, err = store.PagePromotionRules(ctx, PromotionRuleQuery{SourceRepositoryIDs: []string{other.ID}, Limit: 2, PageToken: all.NextPageToken}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("a token of another restriction = %v", err)
	}
	if none, pageErr := store.PagePromotionRules(ctx, PromotionRuleQuery{SourceRepositoryIDs: []string{}}); pageErr != nil || len(none.Items) != 0 {
		t.Fatalf("empty restriction = %#v, %v", none, pageErr)
	}
	if both, pageErr := store.PagePromotionRules(ctx, PromotionRuleQuery{SourceRepositoryIDs: []string{source.ID, other.ID}}); pageErr != nil || len(both.Items) != 4 {
		t.Fatalf("two sources = %#v, %v", both, pageErr)
	}

	// Mutations commit their audit records with them.
	records, err := store.ListAudits(ctx, AuditQuery{Repository: source.Name, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]int{}
	for _, record := range records {
		operations[record.Operation]++
	}
	if operations["promotion_rule.create"] != 1 || operations["promotion_rule.update"] != 1 {
		t.Fatalf("audit operations = %v", operations)
	}

	if err = store.DeletePromotionRule(ctx, updated.ID, audit("promotion_rule.delete")); err != nil {
		t.Fatal(err)
	}
	if err = store.DeletePromotionRule(ctx, updated.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	if _, err = store.GetPromotionRule(ctx, updated.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
	// Room again under the cap.
	if _, err = store.CreatePromotionRule(ctx, local("again"), 3); err != nil {
		t.Fatalf("create after delete = %v", err)
	}
}

func jsonEqual(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	l, _ := json.Marshal(left)
	r, _ := json.Marshal(right)
	return string(l) == string(r)
}

func TestMemoryPromotionRuleStoreAuditFaultLeavesNoRule(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	source, _ := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "fault-source", Format: FormatRaw})
	target, _ := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "fault-target", Format: FormatRaw})
	account, _ := store.CreateServiceAccount(ctx, ServiceAccount{ID: uuid.NewString(), Name: "fault-runner"})
	store.auditFault = func(AuditRecord) error { return errors.New("audit unavailable") }
	rule := PromotionRule{ID: uuid.NewString(), Name: "fault", SourceRepositoryID: source.ID, TargetKind: PromotionTargetLocal, TargetRepositoryID: target.ID,
		Match: json.RawMessage(`{}`), RunAsServiceAccountID: account.ID, CreatedBy: "alice"}
	if _, err := store.CreatePromotionRule(ctx, rule, 5, AuditRecord{Actor: "alice"}); err == nil {
		t.Fatal("a failed audit write must fail the create")
	}
	if page, err := store.PagePromotionRules(ctx, PromotionRuleQuery{}); err != nil || len(page.Items) != 0 {
		t.Fatalf("a rule survived its failed audit: %#v, %v", page, err)
	}
}
