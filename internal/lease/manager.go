package lease

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	budgetpkg "github.com/Dematom1/dibs/internal/budget"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

type Options struct {
	Directory string
	Adapters  adapterpkg.Registry
	Owners    ownerpkg.Registry
	// Version is recorded in saved state as its writer.
	Version string
}
type Manager struct{ options Options }

func New(options Options) *Manager { return &Manager{options: options} }
func ValidID(value string) bool    { return leaseID.MatchString(value) }

type FailureStage uint8

const (
	ResolveDirectory FailureStage = iota
	ReadLedger
	ResolveMachine
	Transition
)

type Failure struct {
	Stage FailureStage
	Cause error
}

func (f *Failure) Error() string { return f.Cause.Error() }
func (f *Failure) Unwrap() error { return f.Cause }
func (m *Manager) directory() (string, error) {
	if m.options.Directory != "" {
		return m.options.Directory, nil
	}
	return stateDir()
}
func (m *Manager) open(ctx context.Context) (*store, string, time.Time, error) {
	dir, err := m.directory()
	if err != nil {
		return nil, "", time.Time{}, &Failure{ResolveDirectory, err}
	}
	s, err := m.openStore(ctx, dir)
	if err != nil {
		return nil, "", time.Time{}, &Failure{ReadLedger, err}
	}
	host, err := machine()
	if err != nil {
		s.Close()
		return nil, "", time.Time{}, &Failure{ResolveMachine, err}
	}
	return s, host, time.Now().UTC(), nil
}
func (m *Manager) checkOwner(reference ownerpkg.Reference, now time.Time) ownerpkg.Liveness {
	return m.options.Owners.Check(context.Background(), reference, now).Liveness
}
func target(l Lease) adapterpkg.Target {
	labels := map[string]string(nil)
	if l.Labels != nil {
		labels = make(map[string]string, len(l.Labels))
		for k, v := range l.Labels {
			labels[k] = v
		}
	}
	return adapterpkg.Target{Ref: l.ResourceRef, Metadata: adapterpkg.Metadata{LeaseID: l.ID, Machine: l.Machine, Owner: l.Owner.String(), Expires: l.Expires, Labels: labels}}
}
func detached(l Lease) Lease {
	l.Labels = target(l).Metadata.Labels
	if l.Reservation != nil {
		job := *l.Reservation
		l.Reservation = &job
	}
	return l
}

type CreateRequest struct {
	Owner       ownerpkg.Reference
	TTL         time.Duration
	Description string
	Labels      map[string]string
	Adapter     string
	File        string
	// Repo and ExcludeRemotes apply to pooled-worktree adapters.
	Repo           string
	ExcludeRemotes []string
}

func (m *Manager) Create(ctx context.Context, r CreateRequest) (Lease, error) {
	s, _, now, err := m.open(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer s.Close()
	if m.checkOwner(r.Owner, now) != ownerpkg.Alive {
		return Lease{}, errors.New("owner became stale or unavailable while waiting; refresh the owner and retry dibs create")
	}
	l, err := newLease(r.Owner, r.TTL, r.Description, r.Labels, now)
	if err != nil {
		return Lease{}, err
	}
	d, ok := m.options.Adapters.Lookup(r.Adapter)
	if !ok {
		return Lease{}, errors.New("unsupported resource adapter")
	}
	snapshotDir := filepath.Join(s.dir, "resources", l.ID)
	l.ResourceRef, err = d.Prepare(ctx, adapterpkg.PrepareRequest{Metadata: target(l).Metadata, SnapshotDir: snapshotDir, File: r.File, Repo: r.Repo, ExcludeRemotes: r.ExcludeRemotes})
	if err != nil {
		return Lease{}, err
	}
	s.Leases[l.ID] = l
	if err := s.Save(); err != nil {
		return Lease{}, err
	}
	ref, createErr := d.Adapter.Create(ctx, target(l))
	// Record what creation learned (such as an allocator token) even when it then failed.
	if !reflect.DeepEqual(ref, l.ResourceRef) {
		if err := d.Validate(snapshotDir, target(l).Metadata, ref); err != nil {
			return Lease{}, fmt.Errorf("creation returned an invalid identity; lease %s retained for cleanup: %w", l.ID, err)
		}
		l.ResourceRef = ref
		s.Leases[l.ID] = l
		if err := s.Save(); err != nil {
			return Lease{}, fmt.Errorf("cannot record the created identity; lease %s retained for cleanup: %w", l.ID, err)
		}
	}
	if createErr != nil {
		return Lease{}, fmt.Errorf("creation incomplete; lease %s retained for cleanup: %w", l.ID, createErr)
	}
	return detached(l), nil
}

type Filter struct{ Owner string }
type ObservedLease struct {
	Lease    Lease
	Liveness ownerpkg.Liveness
}
type Listing struct {
	Leases   []ObservedLease
	At       time.Time
	Reserved budgetpkg.Amount
}

func (m *Manager) List(ctx context.Context, f Filter) (Listing, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return Listing{}, err
	}
	defer s.Close()
	result := Listing{Leases: []ObservedLease{}, At: now}
	selector := ownerpkg.CanonicalSelector(f.Owner)
	for _, id := range s.IDs() {
		l := s.Leases[id]
		if l.Machine == host && l.Reservation != nil {
			result.Reserved.Cores += l.Reservation.Budget.Cores
			result.Reserved.Memory += l.Reservation.Budget.Memory
		}
		if selector != "" && l.Owner.String() != selector {
			continue
		}
		_, live := reclaimable(l, host, now, m.checkOwner)
		result.Leases = append(result.Leases, ObservedLease{detached(l), live})
	}
	return result, nil
}
func (m *Manager) Get(ctx context.Context, id string) (Lease, bool, error) {
	s, _, _, err := m.open(ctx)
	if err != nil {
		return Lease{}, false, err
	}
	defer s.Close()
	l, ok := s.Leases[id]
	return detached(l), ok, nil
}
func (m *Manager) Extend(ctx context.Context, id string, ttl time.Duration) (Lease, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer s.Close()
	l, ok := s.Leases[id]
	if !ok {
		return Lease{}, errors.New("lease does not exist; use dibs create for a new lease")
	}
	if err := extendLease(&l, host, ttl, now, m.checkOwner); err != nil {
		return Lease{}, err
	}
	s.Leases[id] = l
	err = s.Save()
	return detached(l), err
}

type ReleaseRequest struct {
	ID      string
	Confirm bool
	// DiscardUnlanded lets an explicit release discard work an adapter would otherwise protect.
	DiscardUnlanded bool
}
type ReleaseResult struct {
	ID       string
	Exists   bool
	Released bool
}

func (m *Manager) Release(ctx context.Context, r ReleaseRequest) (ReleaseResult, error) {
	s, host, _, err := m.open(ctx)
	if err != nil {
		return ReleaseResult{}, err
	}
	l, exists := s.Leases[r.ID]
	result := ReleaseResult{ID: r.ID, Exists: exists}
	if d, ok := m.options.Adapters.Lookup(l.ResourceRef.Adapter); exists && r.Confirm && ok && d.CleanupOutsideLock && l.Machine == host {
		s.Close()
		result.Released, err = m.releaseOutsideLock(ctx, l, r.DiscardUnlanded)
		return result, err
	}
	defer s.Close()
	if exists && r.Confirm {
		result.Released, err = releaseLease(ctx, s, r.ID, host, m.options.Adapters)
	}
	return result, err
}

// releaseOutsideLock runs slow, conditional cleanup without the host-wide lock
// so other leases' renewals are not stalled, then forgets the lease under the
// lock only if its identity did not change meanwhile.
func (m *Manager) releaseOutsideLock(ctx context.Context, l Lease, discard bool) (bool, error) {
	d, _ := m.options.Adapters.Lookup(l.ResourceRef.Adapter)
	t := target(l)
	t.DiscardUnlanded = discard
	if err := d.Adapter.Destroy(ctx, t); err != nil {
		return false, err
	}
	s, _, _, err := m.open(ctx)
	if err != nil {
		return false, err
	}
	defer s.Close()
	current, ok := s.Leases[l.ID]
	if !ok {
		return true, nil // a concurrent release already forgot it
	}
	if !reflect.DeepEqual(current.ResourceRef, l.ResourceRef) {
		return false, errors.New("lease identity changed during cleanup; lease retained, retry dibs release")
	}
	delete(s.Leases, l.ID)
	if err := s.Save(); err != nil {
		s.Leases[l.ID] = current
		return false, err
	}
	return true, nil
}

type SweepRequest struct{ Confirm bool }

func (m *Manager) Sweep(ctx context.Context, r SweepRequest) (SweepResult, error) {
	s, host, now, err := m.open(ctx)
	if err != nil {
		return SweepResult{}, err
	}
	result, outside := sweep(ctx, s, host, now, r.Confirm, m.checkOwner, m.options.Adapters)
	s.Close()
	for _, l := range outside {
		if _, err := m.releaseOutsideLock(ctx, l, false); err != nil {
			result.Failures[l.ID] = err.Error()
		} else {
			result.Reclaimed = append(result.Reclaimed, l.ID)
		}
	}
	sort.Strings(result.Reclaimed)
	return result, nil
}

type AdmissionRequest struct {
	Owner       ownerpkg.Reference
	Capacity    budgetpkg.Amount
	Request     budgetpkg.Amount
	Class       string
	TTL         time.Duration
	Wait        time.Duration
	Description string
}
type Reservation struct {
	lease     Lease
	directory string
}

func (m *Manager) Admit(ctx context.Context, r AdmissionRequest) (Reservation, error) {
	l, err := newLease(r.Owner, r.TTL, r.Description, nil, time.Now().UTC())
	if err != nil {
		return Reservation{}, &Failure{Transition, err}
	}
	l.ResourceRef.Adapter = "process"
	l.Reservation = &ReservationView{Budget: r.Request, Class: r.Class}
	dir, err := m.directory()
	if err != nil {
		return Reservation{}, &Failure{ResolveDirectory, err}
	}
	l, err = m.admitJob(ctx, dir, l, r.Capacity, r.Wait)
	return Reservation{lease: l, directory: dir}, err
}

type JobResult struct {
	Code    int
	Started bool
	Err     error
}
