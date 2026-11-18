package lease

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	policypkg "github.com/Dematom1/dibs/internal/policy"
)

func scopeManager(t *testing.T) *Manager {
	t.Helper()
	m := testManager()
	m.options.Directory = t.TempDir()
	return m
}

// liveProcess returns a pid owner for a process the test can end.
func liveProcess(t *testing.T) (ownerpkg.Reference, func()) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stop := func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
	t.Cleanup(stop)
	o, err := bindOwner("pid:"+strconv.Itoa(cmd.Process.Pid), 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return o, stop
}

func scopeOwner(id string) ownerpkg.Reference { return ownerpkg.Reference{Kind: ScopeKind, ID: id} }

func TestScopeStartEndAndForget(t *testing.T) {
	ctx := context.Background()
	m := scopeManager(t)
	owner, _ := liveProcess(t)
	scope, done, err := m.StartScope(ctx, ScopeRequest{Owner: owner, TTL: time.Hour, Key: "task-7"})
	if err != nil || done || !scope.IsScope || scope.Key != "task-7" || scope.KeyedBy != ScopeKind {
		t.Fatalf("start = %+v %v %v", scope, done, err)
	}
	again, done, err := m.StartScope(ctx, ScopeRequest{Owner: owner, TTL: 2 * time.Hour, Key: "task-7"})
	if err != nil || !done || again.ID != scope.ID {
		t.Fatalf("keyed retry = %+v %v %v, want the same scope", again, done, err)
	}
	other, _ := liveProcess(t)
	if _, _, err := m.StartScope(ctx, ScopeRequest{Owner: other, TTL: time.Hour, Key: "task-7"}); reason(err) != "key_conflict" {
		t.Fatalf("other owner reused the key: %v", err)
	}
	if _, _, err := m.StartScope(ctx, ScopeRequest{Owner: scopeOwner(scope.ID), TTL: time.Hour}); err == nil {
		t.Fatal("a scope was owned by a scope")
	}
	var ids []string
	for range 2 {
		l, _, err := m.Create(ctx, CreateRequest{Owner: scopeOwner(scope.ID), TTL: time.Hour, Adapter: "none"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, l.ID)
	}
	if v := diskVersion(t, m); v != 7 {
		t.Fatalf("state version with a scope = %d, want 7", v)
	}
	shown, err := m.ShowScope(ctx, "", "task-7")
	if err != nil || shown.Scope.Lease.ID != scope.ID || shown.Scope.Liveness != ownerpkg.Alive || len(shown.Members) != 2 || shown.Members[0].Liveness != ownerpkg.Alive {
		t.Fatalf("show = %+v %v", shown, err)
	}
	if _, err := m.ShowScope(ctx, ids[0], ""); reason(err) != "not_found" {
		t.Fatalf("show of a member: %v", err)
	}
	if _, err := m.Release(ctx, ReleaseRequest{ID: scope.ID, Confirm: true}); reason(err) != HoldScopeMembers {
		t.Fatalf("release of a scope with members: %v", err)
	}
	preview, err := m.EndScope(ctx, scope.ID, false)
	// Newest first; leases created in the same instant fall back to id order, which only needs to be stable.
	if err != nil || preview.Status != "preview" || len(preview.Members) != 2 {
		t.Fatalf("preview = %+v %v", preview, err)
	}
	if _, ok, _ := m.Get(ctx, scope.ID); !ok {
		t.Fatal("preview changed the scope")
	}
	ended, err := m.EndScope(ctx, scope.ID, true)
	if err != nil || ended.Status != "ended" || len(ended.Released) != 2 || len(ended.Members) != 0 || !ended.Forgotten || len(ended.Failures) != 0 {
		t.Fatalf("end = %+v %v", ended, err)
	}
	listing, err := m.List(ctx, Filter{})
	if err != nil || len(listing.Leases) != 0 {
		t.Fatalf("leases after end = %+v %v", listing.Leases, err)
	}
	if v := diskVersion(t, m); v != 2 {
		t.Fatalf("state version after the scope is gone = %d, want 2", v)
	}
	if r, err := m.EndScope(ctx, scope.ID, true); err != nil || r.Status != "already_done" {
		t.Fatalf("second end = %+v %v", r, err)
	}
	fresh, done, err := m.StartScope(ctx, ScopeRequest{Owner: owner, TTL: time.Hour, Key: "task-7"})
	if err != nil || done || fresh.ID == scope.ID {
		t.Fatalf("restart after the scope was forgotten = %+v %v %v", fresh, done, err)
	}
}

func TestEndScopeReleasesNewestFirst(t *testing.T) {
	ctx := context.Background()
	m := scopeManager(t)
	owner, _ := liveProcess(t)
	scope, _, err := m.StartScope(ctx, ScopeRequest{Owner: owner, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.openStore(ctx, m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	for i, at := range []time.Time{testNow, testNow.Add(time.Minute)} {
		l := testLease(t)
		l.Owner, l.Created, l.Expires, l.Description = scopeOwner(scope.ID), at, time.Now().Add(time.Hour), strconv.Itoa(i)
		s.Leases[l.ID] = l
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	r, err := m.EndScope(ctx, scope.ID, false)
	if err != nil || len(r.Members) != 2 {
		t.Fatal(r, err)
	}
	first, _, _ := m.Get(ctx, r.Members[0])
	if first.Description != "1" {
		t.Fatalf("end would release %q first, want the newest", first.Description)
	}
}

func TestScopeOwnerLiveness(t *testing.T) {
	ctx := context.Background()
	m := scopeManager(t)
	owner, stop := liveProcess(t)
	scope, _, err := m.StartScope(ctx, ScopeRequest{Owner: owner, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	member, _, err := m.Create(ctx, CreateRequest{Owner: scopeOwner(scope.ID), TTL: time.Hour, Adapter: "none"})
	if err != nil {
		t.Fatal(err)
	}
	check := func(now time.Time) ownerpkg.Liveness {
		s, err := m.openStore(ctx, m.options.Directory)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		return m.liveness(s, scope.Machine)(member.Owner, now)
	}
	if got := check(time.Now()); got != ownerpkg.Alive {
		t.Fatalf("live scope = %s", got)
	}
	if got := check(time.Now().Add(2 * time.Hour)); got != ownerpkg.Dead {
		t.Fatalf("expired scope = %s", got)
	}
	if got := m.liveness(testStore(t), scope.Machine)(member.Owner, time.Now()); got != ownerpkg.Dead {
		t.Fatalf("absent scope = %s", got)
	}
	// An owner kind this binary cannot check is unknown, and so is a scope it owns while unexpired.
	s, err := m.openStore(ctx, m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	unknown := s.Leases[scope.ID]
	unknown.Owner = ownerpkg.Reference{Kind: "exec", ID: "true"}
	s.Leases[scope.ID] = unknown
	if got := m.liveness(s, scope.Machine)(member.Owner, time.Now()); got != ownerpkg.Unknown {
		t.Fatalf("scope with an unknown owner = %s", got)
	}
	if got := m.liveness(s, scope.Machine)(member.Owner, time.Now().Add(2*time.Hour)); got != ownerpkg.Dead {
		t.Fatalf("expired scope with an unknown owner = %s", got)
	}
	s.Close()
	stop()
	if got := check(time.Now()); got != ownerpkg.Dead {
		t.Fatalf("scope whose owner died = %s", got)
	}
	if _, _, err := m.Create(ctx, CreateRequest{Owner: scopeOwner(scope.ID), TTL: time.Hour, Adapter: "none"}); reason(err) != HoldScopeEnded {
		t.Fatalf("a dead scope took a member: %v", err)
	}
}

func TestSweepReleasesMembersThenTheirScope(t *testing.T) {
	ctx := context.Background()
	m := scopeManager(t)
	owner, stop := liveProcess(t)
	// The scope sorts before its member by id, so only scopes-last ordering lets one pass forget it.
	scope, member := testLease(t), testLease(t)
	scope.ID, scope.Owner, scope.IsScope, scope.Key, scope.KeyedBy, scope.Expires = strings.Repeat("0", 32), owner, true, "job-1", ScopeKind, time.Now().Add(time.Hour)
	member.ID, member.Owner, member.Expires = strings.Repeat("f", 32), scopeOwner(scope.ID), time.Now().Add(time.Hour)
	s, err := m.openStore(ctx, m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	s.Leases = map[string]Lease{scope.ID: scope, member.ID: member}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if r, err := m.Sweep(ctx, SweepRequest{}); err != nil || len(r.Eligible) != 0 {
		t.Fatalf("sweep of a live scope = %+v %v", r, err)
	}
	stop()
	e, err := m.Explain(ctx, scope.ID)
	if err != nil || e.Sweep || e.Verdict.Hold != HoldScopeMembers || !strings.Contains(e.Reason, "still owns leases") {
		t.Fatalf("explain of a dead scope with members = %+v %v", e, err)
	}
	if _, err := m.Reclaim(ctx, scope.ID, true); reason(err) != HoldScopeMembers {
		t.Fatalf("reclaim of a scope with members: %v", err)
	}
	if _, _, err := m.StartScope(ctx, ScopeRequest{Owner: owner, TTL: time.Hour, Key: "job-1"}); reason(err) != HoldScopeEnded {
		t.Fatalf("keyed start of an ended scope: %v", err)
	}
	r, err := m.Sweep(ctx, SweepRequest{Confirm: true})
	if err != nil || len(r.Reclaimed) != 2 || len(r.Failures) != 0 {
		t.Fatalf("one confirmed sweep = %+v %v, want the member and then its scope", r, err)
	}
	for _, id := range []string{member.ID, scope.ID} {
		if _, ok, _ := m.Get(ctx, id); ok {
			t.Fatalf("lease %s survived the sweep", id)
		}
	}
}

func TestSweepHoldsScopeWhileAMemberRemains(t *testing.T) {
	ctx := context.Background()
	m := scopeManager(t)
	owner, stop := liveProcess(t)
	scope, _, err := m.StartScope(ctx, ScopeRequest{Owner: owner, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.openStore(ctx, m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	// Sweep never acts on another machine's lease, so this member outlives the scope's owner.
	foreign := testLease(t)
	foreign.Owner, foreign.Machine = scopeOwner(scope.ID), "elsewhere"
	s.Leases[foreign.ID] = foreign
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	stop()
	r, err := m.Sweep(ctx, SweepRequest{Confirm: true})
	if err != nil || len(r.Reclaimed) != 0 || r.Held[scope.ID] != HoldScopeMembers {
		t.Fatalf("sweep = %+v %v, want the scope held for its member", r, err)
	}
	if _, ok, _ := m.Get(ctx, scope.ID); !ok {
		t.Fatal("a scope with a member was forgotten")
	}
}

func TestScopeRecordsNeedVersionSeven(t *testing.T) {
	s := testStore(t)
	scope := testLease(t)
	scope.IsScope = true
	member := testLease(t)
	member.Owner = scopeOwner(scope.ID)
	s.Leases = map[string]Lease{scope.ID: scope, member.ID: member}
	s.Version = 7
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	s.Version = 6
	if err := s.validate(); err == nil {
		t.Fatal("version 6 accepted a scope")
	}
	s.Version = 7
	for name, broken := range map[string]func(*Lease){
		"scope owned by a scope": func(l *Lease) { l.IsScope = true },
		"malformed scope id":     func(l *Lease) { l.Owner.ID = "nope" },
		"scope keyed by create":  func(l *Lease) { l.IsScope, l.Owner, l.Key, l.KeyedBy = true, scope.Owner, "k", "create" },
		"scope with a resource":  func(l *Lease) { l.IsScope, l.Owner, l.ResourceRef.Adapter = true, scope.Owner, "kind" },
	} {
		bad := member
		broken(&bad)
		s.Leases = map[string]Lease{scope.ID: scope, bad.ID: bad}
		if err := s.validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// An owner-bound scope outlives its TTL while its owner lives, so its members
// stay alive; ending it still makes their owner dead and releases them.
func TestOwnerBoundScope(t *testing.T) {
	ctx := context.Background()
	m := scopeManager(t)
	owner, stop := liveProcess(t)
	bound := &policypkg.Policy{Row: "scope", Binding: policypkg.Owner, TTL: 30 * time.Minute, Action: policypkg.Destroy}
	start := func() (Lease, Lease) {
		scope, _, err := m.StartScope(ctx, ScopeRequest{Owner: owner, TTL: 30 * time.Minute, Policy: func() (*policypkg.Policy, time.Duration, error) { return bound, bound.TTL, nil }})
		if err != nil || scope.Policy == nil || scope.Policy.Row != "scope" {
			t.Fatalf("start = %+v %v", scope, err)
		}
		member, _, err := m.Create(ctx, CreateRequest{Owner: scopeOwner(scope.ID), TTL: time.Hour, Adapter: "none"})
		if err != nil {
			t.Fatal(err)
		}
		return scope, member
	}
	check := func(member Lease, now time.Time) ownerpkg.Liveness {
		s, err := m.openStore(ctx, m.options.Directory)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		return m.liveness(s, member.Machine)(member.Owner, now)
	}
	scope, member := start()
	if got := check(member, time.Now().Add(2*time.Hour)); got != ownerpkg.Alive {
		t.Fatalf("owner-bound scope past its TTL = %s, want alive", got)
	}
	// A member on another machine cannot be released here, so ending keeps the scope.
	s, err := m.openStore(ctx, m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	stuck := s.Leases[member.ID]
	stuck.Machine = "elsewhere"
	s.Leases[member.ID] = stuck
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	ended, err := m.EndScope(ctx, scope.ID, true)
	if err != nil || ended.Forgotten || len(ended.Failures) != 1 {
		t.Fatalf("end = %+v %v", ended, err)
	}
	if got := check(Lease{Owner: member.Owner, Machine: scope.Machine}, time.Now()); got != ownerpkg.Dead {
		t.Fatalf("ended owner-bound scope = %s, want dead", got)
	}
	if _, _, err := m.Create(ctx, CreateRequest{Owner: scopeOwner(scope.ID), TTL: time.Hour, Adapter: "none"}); reason(err) != HoldScopeEnded {
		t.Fatalf("an ended scope took a member: %v", err)
	}
	_, member = start()
	stop()
	if got := check(member, time.Now()); got != ownerpkg.Dead {
		t.Fatalf("owner-bound scope whose owner died = %s", got)
	}
	// A version 7 reader rejects a scope with a policy, so such state is version 8.
	if v := diskVersion(t, m); v != 8 {
		t.Fatalf("state version with a scope policy = %d, want 8", v)
	}
}

// A keyed retry returns the stored scope without consulting the policy, so a
// tightened row cannot refuse it; only a new scope runs the policy.
func TestKeyedScopeRetrySkipsPolicy(t *testing.T) {
	ctx := context.Background()
	m := scopeManager(t)
	owner, _ := liveProcess(t)
	bound := &policypkg.Policy{Row: "scope", Binding: policypkg.Owner, TTL: 20 * time.Minute, Action: policypkg.Destroy}
	scope, _, err := m.StartScope(ctx, ScopeRequest{Owner: owner, Key: "task-7", Policy: func() (*policypkg.Policy, time.Duration, error) { return bound, bound.TTL, nil }})
	if err != nil || scope.TTL != 20*time.Minute {
		t.Fatalf("start = %+v %v", scope, err)
	}
	tightened := errors.New("--ttl 20m0s exceeds the \"scope\" policy's ttl 10m0s")
	refuse := func() (*policypkg.Policy, time.Duration, error) { return nil, 0, tightened }
	again, done, err := m.StartScope(ctx, ScopeRequest{Owner: owner, Key: "task-7", Policy: refuse})
	if err != nil || !done || again.ID != scope.ID {
		t.Fatalf("keyed retry after the row tightened = %+v %v %v, want the stored scope", again, done, err)
	}
	if _, _, err := m.StartScope(ctx, ScopeRequest{Owner: owner, Key: "task-8", Policy: refuse}); !errors.Is(err, tightened) {
		t.Fatalf("new scope under the tightened row: %v", err)
	}
}

// Ending a scope whose grace clock a confirmed sweep started leaves loadable state.
func TestEndScopeClearsGraceClock(t *testing.T) {
	ctx := context.Background()
	m := scopeManager(t)
	owner, stop := liveProcess(t)
	graced := &policypkg.Policy{Row: "scope", Binding: policypkg.Owner, TTL: 30 * time.Minute, Grace: time.Hour, Action: policypkg.Destroy}
	scope, _, err := m.StartScope(ctx, ScopeRequest{Owner: owner, Policy: func() (*policypkg.Policy, time.Duration, error) { return graced, graced.TTL, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Create(ctx, CreateRequest{Owner: scopeOwner(scope.ID), TTL: time.Hour, Adapter: "none"}); err != nil {
		t.Fatal(err)
	}
	stop()
	if _, err := m.Sweep(ctx, SweepRequest{Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if l, _, _ := m.Get(ctx, scope.ID); l.DeadSince == nil {
		t.Fatalf("confirmed sweep did not start the scope's grace clock: %+v", l)
	}
	ended, err := m.EndScope(ctx, scope.ID, true)
	if err != nil || !ended.Forgotten {
		t.Fatalf("end = %+v %v", ended, err)
	}
	if _, err := m.openStore(ctx, m.options.Directory); err != nil {
		t.Fatalf("state after ending a graced scope: %v", err)
	}
}

// A version 7 file cannot hold a scope with a policy.
func TestScopePolicyNeedsVersionEight(t *testing.T) {
	s := testStore(t)
	l := Lease{ID: strings.Repeat("a", 32), Machine: "m", Owner: ownerpkg.Reference{Kind: "pid", ID: "1"}, Created: time.Now(), Expires: time.Now().Add(time.Hour), TTL: time.Hour, IsScope: true, ResourceRef: adapterpkg.ResourceRef{Adapter: "none"},
		Policy: &policypkg.Policy{Row: "scope", Binding: policypkg.Owner, TTL: time.Hour, Action: policypkg.Destroy}}
	s.Leases[l.ID] = l
	s.Version = 7
	if err := s.validate(); err == nil || !strings.Contains(err.Error(), "invalid scope record") {
		t.Fatalf("version 7 with a scope policy = %v", err)
	}
	s.Version = 8
	if err := s.validate(); err != nil {
		t.Fatalf("version 8 with a scope policy = %v", err)
	}
}
