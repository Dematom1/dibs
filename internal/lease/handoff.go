package lease

import (
	"context"
	"time"

	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	policypkg "github.com/Dematom1/dibs/internal/policy"
)

// Handoff records a lease's last change of owner, so explain can say where it came from.
type Handoff struct {
	From ownerpkg.Reference `json:"from"`
	At   time.Time          `json:"at"`
}

type HandoffRequest struct {
	ID      string
	Owner   ownerpkg.Reference
	Confirm bool
	Policy  func(resource, ownerKind string) (*policypkg.Policy, error)
}

type HandoffResult struct {
	ID string
	// Status is preview, handed_off, or already_done when the lease already has this owner.
	Status string
	From   ownerpkg.Reference
	// Liveness is the new owner's, which must be alive for the handoff.
	Liveness ownerpkg.Liveness
}

// Handoff moves a lease to a new owner proven alive, keeping its resource and
// expiry. A lease handed out of a scope is no longer its member.
func (m *Manager) Handoff(ctx context.Context, r HandoffRequest) (HandoffResult, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return HandoffResult{}, err
	}
	defer s.Close()
	l, ok := s.Leases[r.ID]
	switch {
	case !ok:
		return HandoffResult{}, refuse("not_found", "lease "+r.ID+" does not exist on this machine")
	case l.Machine != host:
		return HandoffResult{}, refuse("wrong_machine", "run dibs handoff on the lease's machine")
	}
	result := HandoffResult{ID: r.ID, Status: "preview", From: l.Owner}
	if sameOwner(l.Owner, r.Owner) {
		result.Status, result.Liveness = "already_done", m.liveness(s, host)(r.Owner, now)
		return result, nil
	}
	switch {
	case l.Releasing != nil:
		return HandoffResult{}, refuse(CleanupInProgress, "cleanup of lease "+r.ID+" is in progress; it cannot change owner")
	case l.Reservation != nil:
		return HandoffResult{}, refuse("job_lease", "lease "+r.ID+" belongs to a job and ends with it; it cannot change owner")
	case l.IsScope && r.Owner.Kind == ScopeKind:
		return HandoffResult{}, refuse("scope_nested", "lease "+r.ID+" is a scope, and a scope cannot be owned by another scope")
	}
	check := m.liveness(s, host)
	result.Liveness = check(r.Owner, now)
	if result.Liveness != ownerpkg.Alive {
		return HandoffResult{}, &Refusal{Reason: "owner_not_alive", Message: "new owner " + r.Owner.String() + " is " + string(result.Liveness) + "; a lease passes only to an owner proven alive"}
	}
	next := l
	next.Owner, next.DeadSince, next.Handoff = r.Owner, nil, &Handoff{From: l.Owner, At: now}
	// A live owner does not revive an expired time-bound lease, which sweep would still reclaim.
	if ended, _ := reclaimable(next, host, now, check); ended {
		return HandoffResult{}, refuse("expired", "lease "+r.ID+" has expired; renew it with dibs extend before handing it off, or release it")
	}
	if !r.Confirm {
		return result, nil
	}
	if r.Policy != nil {
		resource := l.ResourceRef.Adapter
		if l.IsScope {
			resource = "scope"
		}
		next.Policy, err = r.Policy(resource, r.Owner.Kind)
		if err != nil {
			return HandoffResult{}, err
		}
	}
	s.Leases[r.ID] = next
	if err := s.Save(); err != nil {
		return HandoffResult{}, err
	}
	result.Status = "handed_off"
	return result, nil
}
