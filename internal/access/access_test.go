package access

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/mlops-ca/notarx/internal/repository"
)

type gate struct {
	enabled bool
	err     error
}

func (g gate) GetPublicAccessPolicy(context.Context) (repository.PublicAccessPolicy, error) {
	return repository.PublicAccessPolicy{Enabled: g.enabled}, g.err
}

var (
	visibilities = []repository.Visibility{repository.VisibilityPrivate, repository.VisibilityAuthenticated, repository.VisibilityPublic}
	classes      = []Class{Anonymous, Authenticated, Granted}
	methods      = []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete}
	formats      = []repository.Format{
		repository.FormatOCI, repository.FormatMaven, repository.FormatConan, repository.FormatRaw, repository.FormatNPM,
		repository.FormatPyPI, repository.FormatGo, repository.FormatCargo, repository.FormatAPT, repository.FormatNuGet,
		repository.FormatHelm, repository.FormatRubyGems,
	}
)

// want is the independent oracle for the ADR 0029 table: private reads need a
// grant, authenticated reads need any identity, public reads need the gate for an
// anonymous caller, and only a grant ever writes.
func want(class Class, visibility repository.Visibility, gateOpen bool, method string) bool {
	if class == Granted {
		return true
	}
	if !IsRead(method) {
		return false
	}
	switch visibility {
	case repository.VisibilityPublic:
		return class == Authenticated || gateOpen
	case repository.VisibilityAuthenticated:
		return class == Authenticated
	default:
		return false
	}
}

func TestDecideRepositoryMatrix(t *testing.T) {
	ctx := context.Background()
	for _, format := range formats {
		for _, visibility := range visibilities {
			for _, open := range []bool{false, true} {
				for _, class := range classes {
					for _, method := range methods {
						repo := repository.HostedRepository{ID: "r", Name: "n", Format: format, State: repository.RepositoryActive, Visibility: visibility}
						decision, members, err := Decide(ctx, ReadRequest{Class: class, Method: method, Gate: gate{enabled: open}, Repository: &repo})
						if err != nil || len(members.Members) != 0 {
							t.Fatalf("%s/%s: err=%v members=%v", format, visibility, err, members)
						}
						if decision.Allow != want(class, visibility, open, method) {
							t.Errorf("%s visibility=%s gate=%v class=%d %s: allow=%v", format, visibility, open, class, method, decision.Allow)
						}
						if decision.Allow == (decision.Deny != DenyNone) {
							t.Errorf("allow=%v must pair with deny kind %d", decision.Allow, decision.Deny)
						}
					}
				}
			}
		}
	}
}

func TestDecideDenialIsIndistinguishableFromAbsent(t *testing.T) {
	ctx := context.Background()
	for _, class := range classes {
		for _, method := range methods {
			if class == Granted {
				continue
			}
			repo := repository.HostedRepository{State: repository.RepositoryActive, Visibility: repository.VisibilityPrivate}
			private, _, _ := Decide(ctx, ReadRequest{Class: class, Method: method, Gate: gate{enabled: true}, Repository: &repo})
			if absent := Absent(class, method); private != absent {
				t.Errorf("class=%d %s: private %+v differs from absent %+v", class, method, private, absent)
			}
		}
	}
	if d := Absent(Anonymous, http.MethodGet); d.Deny != Challenge401 {
		t.Fatalf("an anonymous denial must be the 401 challenge, got %d", d.Deny)
	}
	if d := Absent(Authenticated, http.MethodGet); d.Deny != NotFound404 {
		t.Fatalf("an authenticated denial must be the 404 of a missing resource, got %d", d.Deny)
	}
}

func TestDecideFailsClosed(t *testing.T) {
	ctx := context.Background()
	public := repository.HostedRepository{State: repository.RepositoryActive, Visibility: repository.VisibilityPublic}
	for name, source := range map[string]any{
		"no gate":          nil,
		"gate read error":  gate{enabled: true, err: errors.New("database down")},
		"not a gate":       struct{}{},
		"closed gate":      gate{enabled: false},
		"typed nil gate":   (*gate)(nil),
		"gate and no rows": gate{},
	} {
		if d, _, _ := Decide(ctx, ReadRequest{Class: Anonymous, Method: http.MethodGet, Gate: source, Repository: &public}); d.Allow {
			t.Errorf("%s admitted an anonymous read of a public repository", name)
		}
	}
	// A public target behind a closed gate behaves as authenticated: the gate never widens access.
	if d, _, _ := Decide(ctx, ReadRequest{Class: Authenticated, Method: http.MethodGet, Gate: nil, Repository: &public}); !d.Allow {
		t.Fatal("an authenticated caller must still read a public repository when the gate is closed")
	}
	// A denial carries no visibility, so it matches the denial of an absent name.
	if d, _, _ := Decide(ctx, ReadRequest{Class: Anonymous, Method: http.MethodGet, Gate: gate{enabled: false}, Repository: &public}); d != Absent(Anonymous, http.MethodGet) {
		t.Fatalf("a public repository behind a closed gate must be denied exactly like an absent one: %+v", d)
	}
	if d, _, _ := Decide(ctx, ReadRequest{Class: Anonymous, Method: http.MethodGet, Gate: gate{enabled: true}, Repository: &public}); d.Visibility != repository.VisibilityPublic {
		t.Fatalf("effective visibility = %q, want public while the gate is open", d.Visibility)
	}
	for _, unknown := range []repository.Visibility{"", "internal", "PUBLIC"} {
		repo := repository.HostedRepository{State: repository.RepositoryActive, Visibility: unknown}
		for _, class := range classes {
			if d, _, _ := Decide(ctx, ReadRequest{Class: class, Method: http.MethodGet, Gate: gate{enabled: true}, Repository: &repo}); d.Allow {
				t.Errorf("unknown visibility %q admitted class %d", unknown, class)
			}
		}
	}
	for _, state := range []repository.RepositoryState{repository.RepositoryDeleting, repository.RepositoryDeleted, ""} {
		repo := repository.HostedRepository{State: state, Visibility: repository.VisibilityPublic}
		if d, _, _ := Decide(ctx, ReadRequest{Class: Anonymous, Method: http.MethodGet, Gate: gate{enabled: true}, Repository: &repo}); d.Allow {
			t.Errorf("state %q admitted an anonymous read", state)
		}
	}
}

func TestDecideRejectsRequestsWithoutExactlyOneTarget(t *testing.T) {
	repo := repository.HostedRepository{}
	group := repository.HostedGroup{}
	for name, req := range map[string]ReadRequest{
		"none": {Class: Granted},
		"two":  {Class: Granted, Repository: &repo, Group: &group},
	} {
		d, _, err := Decide(context.Background(), req)
		if !errors.Is(err, ErrInvalidRequest) || d.Allow {
			t.Errorf("%s: allow=%v err=%v", name, d.Allow, err)
		}
	}
}

func member(name string, visibility repository.Visibility) repository.Member {
	return BindMember(repository.Member{Name: name, RepositoryID: name}, repository.HostedRepository{Visibility: visibility})
}

func TestGroupVisibilityIsACeilingAndMembersFilterPerClass(t *testing.T) {
	ctx := context.Background()
	members := []repository.Member{
		member("private", repository.VisibilityPrivate),
		member("authenticated", repository.VisibilityAuthenticated),
		member("public", repository.VisibilityPublic),
	}
	names := func(in []repository.Member) []string {
		out := []string{}
		for _, m := range in {
			out = append(out, m.Name)
		}
		return out
	}
	for _, tc := range []struct {
		group repository.Visibility
		class Class
		open  bool
		want  []string
	}{
		{repository.VisibilityPublic, Anonymous, true, []string{"public"}},
		{repository.VisibilityPublic, Anonymous, false, nil},
		{repository.VisibilityPublic, Authenticated, false, []string{"authenticated", "public"}},
		{repository.VisibilityPublic, Granted, false, []string{"private", "authenticated", "public"}},
		{repository.VisibilityAuthenticated, Anonymous, true, nil},
		{repository.VisibilityAuthenticated, Authenticated, true, []string{"authenticated", "public"}},
		{repository.VisibilityPrivate, Authenticated, true, nil},
		{repository.VisibilityPrivate, Granted, true, []string{"private", "authenticated", "public"}},
	} {
		group := repository.HostedGroup{Visibility: tc.group}
		d, resolved, err := Decide(ctx, ReadRequest{Class: tc.class, Method: http.MethodGet, Gate: gate{enabled: tc.open}, Group: &group, Members: members})
		got := names(resolved.Members)
		if err != nil || len(got) != len(tc.want) || (len(got) > 0 && !d.Allow) {
			t.Errorf("group=%s class=%d gate=%v: members %v, want %v (allow=%v err=%v)", tc.group, tc.class, tc.open, got, tc.want, d.Allow, err)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("group=%s class=%d gate=%v: members %v, want %v", tc.group, tc.class, tc.open, got, tc.want)
			}
		}
	}
	if got := AnonymousGroupMembers(ctx, gate{enabled: true}, repository.HostedGroup{Visibility: repository.VisibilityPrivate}, members); len(got) != 0 {
		t.Fatalf("a private group exposed members to an anonymous caller: %v", got)
	}
}

func TestLegacyGroupDecision(t *testing.T) {
	ctx := context.Background()
	open := gate{enabled: true}
	members := []repository.Member{{Name: "a"}, {Name: "b"}}
	for name, tc := range map[string]struct {
		group repository.Group
		want  bool
	}{
		"enabled and anonymous": {repository.Group{Enabled: true, Anonymous: true, Members: members}, true},
		"private":               {repository.Group{Enabled: true, Members: members}, false},
		"disabled":              {repository.Group{Anonymous: true, Members: members}, false},
	} {
		if got := AnonymousLegacyGroupRead(ctx, open, tc.group, http.MethodGet); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
	if AnonymousLegacyGroupRead(ctx, gate{}, repository.Group{Enabled: true, Anonymous: true, Members: members}, http.MethodGet) {
		t.Error("a closed gate admitted a legacy group")
	}
	// The group's visibility is the ceiling and every member of an admitted
	// legacy group resolves: members carry no visibility of their own.
	decision, resolved, err := Decide(ctx, ReadRequest{Class: Anonymous, Method: http.MethodGet, Gate: open, LegacyGroup: &repository.Group{Enabled: true, Anonymous: true, Members: members}})
	if err != nil || !decision.Allow || len(resolved.Members) != 2 {
		t.Fatalf("legacy resolution = %+v %v %v", decision, resolved, err)
	}
}

func TestLegacyGroupClasses(t *testing.T) {
	ctx := context.Background()
	members := []repository.Member{{Name: "a"}}
	public := repository.Group{Enabled: true, Anonymous: true, Members: members}
	private := repository.Group{Enabled: true, Members: members}
	for name, tc := range map[string]struct {
		group repository.Group
		class Class
		open  bool
		want  bool
	}{
		"anonymous, open gate":                 {public, Anonymous, true, true},
		"anonymous, closed gate":               {public, Anonymous, false, false},
		"authenticated reads a public group":   {public, Authenticated, false, true},
		"authenticated, private group":         {private, Authenticated, true, false},
		"granted reads a private group":        {private, Granted, false, true},
		"granted, disabled group still passes": {repository.Group{Members: members}, Granted, false, true},
		"authenticated, disabled group":        {repository.Group{Anonymous: true, Members: members}, Authenticated, true, false},
	} {
		d, _, err := Decide(ctx, ReadRequest{Class: tc.class, Method: http.MethodGet, Gate: gate{enabled: tc.open}, LegacyGroup: &tc.group})
		if err != nil || d.Allow != tc.want {
			t.Errorf("%s: allow=%v err=%v, want %v", name, d.Allow, err, tc.want)
		}
	}
}

func TestAnonymousGroupFullyPublicUsesOneDecision(t *testing.T) {
	ctx := context.Background()
	group := repository.HostedGroup{Visibility: repository.VisibilityPublic}
	both := []repository.Member{member("a", repository.VisibilityPublic), member("b", repository.VisibilityPublic)}
	mixed := []repository.Member{member("a", repository.VisibilityPublic), member("b", repository.VisibilityAuthenticated)}
	if !AnonymousGroupFullyPublic(ctx, FixedGate(true), group, both) || AnonymousGroupFullyPublic(ctx, FixedGate(true), group, mixed) || AnonymousGroupFullyPublic(ctx, FixedGate(false), group, both) {
		t.Fatal("a group is fully public only when the gate is open and every member is public")
	}
}

func TestDescribeAnonymousReasons(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		repo   repository.HostedRepository
		gate   any
		reason string
		allow  bool
	}{
		"enabled":      {repository.HostedRepository{State: repository.RepositoryActive, Visibility: repository.VisibilityPublic}, gate{enabled: true}, ReasonRepositoryEnabled, true},
		"not active":   {repository.HostedRepository{State: repository.RepositoryDeleted, Visibility: repository.VisibilityPublic}, gate{enabled: true}, ReasonRepositoryNotActive, false},
		"gate closed":  {repository.HostedRepository{State: repository.RepositoryActive, Visibility: repository.VisibilityPublic}, gate{}, ReasonGateDisabled, false},
		"not public":   {repository.HostedRepository{State: repository.RepositoryActive, Visibility: repository.VisibilityPrivate}, gate{enabled: true}, ReasonRepositoryDisabled, false},
		"closed gate+": {repository.HostedRepository{State: repository.RepositoryActive, Visibility: repository.VisibilityPrivate}, gate{}, ReasonGateDisabled, false},
	} {
		got := DescribeAnonymous(ctx, tc.gate, tc.repo)
		if got.Reason != tc.reason || got.Allowed != tc.allow || got.Visibility != tc.repo.Visibility {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestFixedGateSnapshotsOnePolicyReading(t *testing.T) {
	ctx := context.Background()
	if !GateOpen(ctx, FixedGate(true)) || GateOpen(ctx, FixedGate(false)) {
		t.Fatal("a fixed gate must report the snapshot it was built with")
	}
	public := repository.HostedRepository{State: repository.RepositoryActive, Visibility: repository.VisibilityPublic}
	if !AnonymousRepositoryRead(ctx, FixedGate(true), public, http.MethodGet) || AnonymousRepositoryRead(ctx, FixedGate(false), public, http.MethodGet) {
		t.Fatal("anonymous repository read must follow the gate")
	}
}
