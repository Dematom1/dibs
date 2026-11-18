package lease

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	policypkg "github.com/Dematom1/dibs/internal/policy"
)

// ScopeKind names the owner kind a scope lends its members: scope:<scope lease id>.
const ScopeKind = "scope"

// ScopeOwner registers scope:<id>. Its liveness lives in the ledger, so the
// manager resolves it there; this registry check alone never permits reclaim.
func ScopeOwner() ownerpkg.Definition {
	return ownerpkg.Definition{Name: ScopeKind,
		Bind: func(_ context.Context, value string, _ time.Duration, _ time.Time) (ownerpkg.Reference, error) {
			id := strings.TrimPrefix(value, ScopeKind+":")
			if !ValidID(id) {
				return ownerpkg.Reference{}, errors.New("scope owner must be scope:<id>, the id dibs scope start printed")
			}
			return ownerpkg.Reference{Kind: ScopeKind, ID: id}, nil
		},
		Check: func(context.Context, ownerpkg.Reference, time.Time) ownerpkg.Observation {
			return ownerpkg.Observation{Liveness: ownerpkg.Unknown}
		},
	}
}

// liveness checks owners against this ledger: scope:<id> is alive while that
// scope lease exists, is unexpired and its own owner is alive; dead once it is
// absent or reclaimable; unknown while its own owner is unknown.
func (m *Manager) liveness(s *store, host string) func(ownerpkg.Reference, time.Time) ownerpkg.Liveness {
	return func(o ownerpkg.Reference, now time.Time) ownerpkg.Liveness {
		if o.Kind != ScopeKind {
			return m.checkOwner(o, now)
		}
		scope, ok := s.Leases[o.ID]
		if !ok || !scope.IsScope {
			return ownerpkg.Dead
		}
		// Scopes never nest, so the scope's own owner resolves through the registry.
		if ended, live := reclaimable(scope, host, now, m.checkOwner); ended {
			return ownerpkg.Dead
		} else {
			return live
		}
	}
}

// ownerNotAlive explains why a new lease cannot take this owner.
func ownerNotAlive(o ownerpkg.Reference, fallback string) error {
	if o.Kind == ScopeKind {
		return refuse(HoldScopeEnded, "scope "+o.ID+" is absent, ended, or its owner is not proven alive; only a live scope can own a lease")
	}
	return errors.New(fallback)
}

// members lists the leases a scope owns, newest first, which is the order scope end releases them.
func members(s *store, id string) []Lease {
	var owned []Lease
	for _, l := range s.Leases {
		if l.Owner.Kind == ScopeKind && l.Owner.ID == id {
			owned = append(owned, l)
		}
	}
	sort.Slice(owned, func(i, j int) bool {
		if !owned[i].Created.Equal(owned[j].Created) {
			return owned[i].Created.After(owned[j].Created)
		}
		return owned[i].ID > owned[j].ID
	})
	return owned
}

// holdScope keeps a scope on the books while it still has members: a scope is
// forgotten only once nothing it accounts for exists.
func holdScope(s *store, l Lease, v *Verdict) {
	if v.Reclaim && l.IsScope && len(members(s, l.ID)) > 0 {
		v.Reclaim, v.Hold = false, HoldScopeMembers
	}
}

type ScopeRequest struct {
	Owner ownerpkg.Reference
	TTL   time.Duration
	// Key is the caller's own id for the work; the same key returns the same live scope.
	Key string
	// Policy is the scope's own lifetime row, when lease policies are on.
	Policy *policypkg.Policy
}

// StartScope gets or creates a scope. It reports true when Key named a live
// scope that already existed and nothing was done.
func (m *Manager) StartScope(ctx context.Context, r ScopeRequest) (Lease, bool, error) {
	if r.Owner.Kind == ScopeKind {
		return Lease{}, false, errors.New("a scope cannot be owned by another scope")
	}
	s, host, now, err := m.open(ctx)
	if err != nil {
		return Lease{}, false, err
	}
	defer s.Close()
	l, done, err := keyed(s, host, r.Key, func(l Lease) bool { return l.IsScope && sameOwner(l.Owner, r.Owner) })
	if err != nil {
		return Lease{}, false, err
	}
	if done {
		if ended, _ := reclaimable(l, host, now, m.checkOwner); ended {
			return Lease{}, false, refuse(HoldScopeEnded, "key "+r.Key+" names scope "+l.ID+", which has ended; finish it with dibs scope end "+l.ID+" --confirm, or choose a new --key")
		}
		return l, true, nil
	}
	if m.checkOwner(r.Owner, now) != ownerpkg.Alive {
		return Lease{}, false, errors.New("owner became stale or unavailable while waiting; refresh the owner and retry dibs scope start")
	}
	description := "scope"
	if r.Key != "" {
		description += " " + r.Key
	}
	if l, err = newLease(r.Owner, r.TTL, description, nil, now); err != nil {
		return Lease{}, false, err
	}
	l.IsScope, l.Key, l.Policy = true, r.Key, r.Policy
	if r.Key != "" {
		l.KeyedBy = ScopeKind
	}
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		return Lease{}, false, err
	}
	return detached(l), false, nil
}

type ScopeView struct {
	At      time.Time
	Scope   ObservedLease
	Members []ObservedLease
}

// ShowScope reads a scope and its members by id, or by key when id is empty.
func (m *Manager) ShowScope(ctx context.Context, id, key string) (ScopeView, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return ScopeView{}, err
	}
	defer s.Close()
	if id == "" {
		for _, candidate := range s.IDs() {
			if l := s.Leases[candidate]; l.IsScope && l.Key == key && l.Machine == host {
				id = candidate
			}
		}
	}
	scope, ok := s.Leases[id]
	if !ok || !scope.IsScope {
		name := id
		if name == "" {
			name = "for key " + key
		}
		return ScopeView{}, refuse("not_found", "no scope "+name+" exists on this machine")
	}
	check := m.liveness(s, host)
	observe := func(l Lease) ObservedLease {
		_, live := reclaimable(l, host, now, check)
		return ObservedLease{detached(l), live}
	}
	v := ScopeView{At: now, Scope: observe(scope), Members: []ObservedLease{}}
	for _, l := range members(s, id) {
		v.Members = append(v.Members, observe(l))
	}
	return v, nil
}

type ScopeEnd struct {
	ID string
	// Status is preview, ended, or already_done when no such scope remains.
	Status string
	// Members are the leases the scope owns: those a preview would release, or those still retained after --confirm.
	Members  []string
	Released []string
	Failures map[string]string
	// Forgotten reports that the scope lease itself is gone because it has no members left.
	Forgotten bool
}

// EndScope previews, or with confirm performs: expire the scope, release each
// member newest first with the ordinary release, and forget the scope once it
// has no members. A failed release keeps that member and the scope; a retry converges.
func (m *Manager) EndScope(ctx context.Context, id string, confirm bool) (ScopeEnd, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return ScopeEnd{}, err
	}
	scope, ok := s.Leases[id]
	r := ScopeEnd{ID: id, Status: "preview", Members: []string{}, Released: []string{}, Failures: map[string]string{}}
	switch {
	case !ok:
		s.Close()
		r.Status = "already_done"
		return r, nil
	case !scope.IsScope:
		s.Close()
		return ScopeEnd{}, refuse("not_scope", "lease "+id+" is not a scope; use dibs release "+id)
	case scope.Machine != host:
		s.Close()
		return ScopeEnd{}, refuse("wrong_machine", "run dibs scope end on the scope's machine")
	}
	owned := members(s, id)
	for _, l := range owned {
		r.Members = append(r.Members, l.ID)
	}
	if !confirm {
		s.Close()
		return r, nil
	}
	r.Status = "ended"
	// Expiring first makes every member's owner dead, so no new member can join while they are released.
	// An owner-bound scope ignores expiry, so ending it also drops its policy.
	if now.Before(scope.Expires) || scope.Policy != nil {
		if now.Before(scope.Expires) {
			scope.Expires = now
		}
		scope.Policy = nil
		s.Leases[id] = scope
		if err := s.Save(); err != nil {
			s.Close()
			return ScopeEnd{}, err
		}
	}
	s.Close()
	for _, l := range owned {
		released, err := m.Release(ctx, ReleaseRequest{ID: l.ID, Confirm: true, Contract: true})
		if err != nil {
			r.Failures[l.ID] = err.Error()
		} else if released.Released {
			r.Released = append(r.Released, l.ID)
		}
	}
	s, host, _, err = m.open(ctx)
	if err != nil {
		return r, err
	}
	defer s.Close()
	r.Members = []string{}
	for _, l := range members(s, id) {
		r.Members = append(r.Members, l.ID)
	}
	if _, ok := s.Leases[id]; !ok {
		r.Forgotten = true
	} else if len(r.Members) == 0 {
		if r.Forgotten, err = releaseLease(ctx, s, id, host, m.options.Adapters); err != nil {
			return r, err
		}
	}
	return r, nil
}
