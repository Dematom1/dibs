package lease

import (
	"context"
	"strings"
	"testing"
	"time"

	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	policypkg "github.com/Dematom1/dibs/internal/policy"
)

func TestHandoffMovesALeaseOutOfItsScope(t *testing.T) {
	ctx := context.Background()
	m := scopeManager(t)
	task, _ := liveProcess(t)
	scope, _, err := m.StartScope(ctx, ScopeRequest{Owner: task, TTL: time.Hour, Key: "task-7"})
	if err != nil {
		t.Fatal(err)
	}
	worktree, _, err := m.Create(ctx, CreateRequest{Owner: scopeOwner(scope.ID), TTL: time.Hour, Adapter: "none"})
	if err != nil {
		t.Fatal(err)
	}
	pr, _ := liveProcess(t)
	preview, err := m.Handoff(ctx, HandoffRequest{ID: worktree.ID, Owner: pr})
	if err != nil || preview.Status != "preview" || preview.Liveness != ownerpkg.Alive || preview.From != scopeOwner(scope.ID) {
		t.Fatalf("preview = %+v %v", preview, err)
	}
	if l, _, _ := m.Get(ctx, worktree.ID); l.Owner != scopeOwner(scope.ID) || l.Handoff != nil {
		t.Fatalf("preview changed the lease: %+v", l)
	}
	if v := diskVersion(t, m); v != 7 {
		t.Fatalf("state version before a handoff = %d, want 7", v)
	}
	for range 2 {
		if _, err := m.Handoff(ctx, HandoffRequest{ID: worktree.ID, Owner: pr, Confirm: true, Policy: func(resource, ownerKind string) (*policypkg.Policy, error) {
			if resource != "none" || ownerKind != "pid" {
				t.Fatalf("policy lookup = %s/%s", resource, ownerKind)
			}
			table, err := policypkg.Table(nil, policypkg.Kinds{Resources: []string{"none"}, Owners: []string{"pid"}})
			if err != nil {
				return nil, err
			}
			p, ok := policypkg.Resolve(table, resource, ownerKind)
			if !ok {
				t.Fatalf("no policy for %s/%s", resource, ownerKind)
			}
			return &p, nil
		}}); err != nil {
			t.Fatal(err)
		}
	}
	l, _, _ := m.Get(ctx, worktree.ID)
	if l.Policy == nil || l.Policy.Row != "none" || l.Policy.Binding != policypkg.Time || l.Policy.TTL != 8*time.Hour {
		t.Fatalf("handed-off policy = %+v", l.Policy)
	}
	if !sameOwner(l.Owner, pr) || l.Handoff == nil || l.Handoff.From != scopeOwner(scope.ID) || l.Handoff.At.IsZero() || !l.Expires.Equal(worktree.Expires) {
		t.Fatalf("handed-off lease = %+v", l)
	}
	if v := diskVersion(t, m); v != 8 {
		t.Fatalf("state version with a handoff = %d, want 8", v)
	}
	again, err := m.Handoff(ctx, HandoffRequest{ID: worktree.ID, Owner: pr, Confirm: true})
	if err != nil || again.Status != "already_done" {
		t.Fatalf("retry = %+v %v", again, err)
	}
	if after, _, _ := m.Get(ctx, worktree.ID); !after.Handoff.At.Equal(l.Handoff.At) {
		t.Fatalf("retry rewrote the handoff record: %+v", after.Handoff)
	}
	ended, err := m.EndScope(ctx, scope.ID, true)
	if err != nil || len(ended.Released) != 0 || !ended.Forgotten {
		t.Fatalf("end = %+v %v, want the scope forgotten and the handed-off lease kept", ended, err)
	}
	if _, ok, _ := m.Get(ctx, worktree.ID); !ok {
		t.Fatal("scope end released a lease handed out of it")
	}
	e, err := m.Explain(ctx, worktree.ID)
	if err != nil || e.Lease.Handoff == nil || e.Liveness != ownerpkg.Alive {
		t.Fatalf("explain = %+v %v", e, err)
	}
}

func TestHandoffRefusalsChangeNothing(t *testing.T) {
	ctx := context.Background()
	m := scopeManager(t)
	owner, _ := liveProcess(t)
	dead, stop := liveProcess(t)
	stop()
	scope, _, err := m.StartScope(ctx, ScopeRequest{Owner: owner, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	l, _, err := m.Create(ctx, CreateRequest{Owner: owner, TTL: time.Hour, Adapter: "none"})
	if err != nil {
		t.Fatal(err)
	}
	expired, _, err := m.Create(ctx, CreateRequest{Owner: owner, TTL: time.Nanosecond, Adapter: "none"})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := liveProcess(t)
	for name, c := range map[string]struct {
		r    HandoffRequest
		want string
	}{
		"dead owner":    {HandoffRequest{ID: l.ID, Owner: dead, Confirm: true}, "owner_not_alive"},
		"absent scope":  {HandoffRequest{ID: l.ID, Owner: scopeOwner(l.ID), Confirm: true}, "owner_not_alive"},
		"missing lease": {HandoffRequest{ID: strings.Repeat("e", 32), Owner: other, Confirm: true}, "not_found"},
		"nested scope":  {HandoffRequest{ID: scope.ID, Owner: scopeOwner(scope.ID), Confirm: true}, "scope_nested"},
		"expired lease": {HandoffRequest{ID: expired.ID, Owner: other, Confirm: true}, "expired"},
	} {
		if _, err := m.Handoff(ctx, c.r); reason(err) != c.want {
			t.Fatalf("%s: %v, want refusal %s", name, err, c.want)
		}
	}
	if v := diskVersion(t, m); v != 7 {
		t.Fatalf("state version after refusals = %d, want 7", v)
	}
	// A scope itself may pass to a new owner, so a restarted caller can keep it.
	if r, err := m.Handoff(ctx, HandoffRequest{ID: scope.ID, Owner: other, Confirm: true}); err != nil || r.Status != "handed_off" {
		t.Fatalf("scope handoff = %+v %v", r, err)
	}
}

func TestHandoffRecordNeedsVersionEight(t *testing.T) {
	s := testStore(t)
	l := testLease(t)
	l.Handoff = &Handoff{From: ownerpkg.Reference{Kind: "pid", ID: "1"}, At: testNow}
	s.Leases[l.ID] = l
	s.Version = 7
	if err := s.validate(); err == nil {
		t.Fatal("a version 7 ledger carried a handoff record")
	}
	s.Version = 8
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
}

// Expiry is judged by the policy the handoff would save: an expired owner-bound
// lease may pass to an owner whose row keeps it owner-bound, but not to one whose
// row is time-bound, which sweep would reclaim at once.
func TestHandoffChecksExpiryUnderTheNewPolicy(t *testing.T) {
	ctx := context.Background()
	m := scopeManager(t)
	task, _ := liveProcess(t)
	scope, _, err := m.StartScope(ctx, ScopeRequest{Owner: task, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ownerBound := &policypkg.Policy{Row: "none/scope", Binding: policypkg.Owner, TTL: time.Millisecond, Action: policypkg.Destroy}
	l, _, err := m.Create(ctx, CreateRequest{Owner: scopeOwner(scope.ID), TTL: time.Millisecond, Adapter: "none", Policy: ownerBound})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	next, _ := liveProcess(t)
	timeBound := func(string, string) (*policypkg.Policy, error) {
		return &policypkg.Policy{Row: "none", Binding: policypkg.Time, TTL: 8 * time.Hour, Action: policypkg.Destroy}, nil
	}
	for _, confirm := range []bool{false, true} {
		if _, err := m.Handoff(ctx, HandoffRequest{ID: l.ID, Owner: next, Confirm: confirm, Policy: timeBound}); reason(err) != "expired" {
			t.Fatalf("expired lease handed to a time-bound row (confirm %v): %v", confirm, err)
		}
	}
	if got, _, _ := m.Get(ctx, l.ID); !sameOwner(got.Owner, scopeOwner(scope.ID)) || got.Policy.Row != "none/scope" {
		t.Fatalf("refused handoff changed the lease: %+v", got)
	}
	keepsOwnerBound := func(string, string) (*policypkg.Policy, error) {
		return &policypkg.Policy{Row: "none/pid", Binding: policypkg.Owner, TTL: time.Hour, Action: policypkg.Destroy}, nil
	}
	if r, err := m.Handoff(ctx, HandoffRequest{ID: l.ID, Owner: next, Confirm: true, Policy: keepsOwnerBound}); err != nil || r.Status != "handed_off" {
		t.Fatalf("expired lease handed to an owner-bound row = %+v %v", r, err)
	}
}
