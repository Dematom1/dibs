package lease

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	nonepkg "github.com/Dematom1/dibs/internal/adapter/none"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

// scripted is a slow, conditional adapter whose cleanup runs outside the state lock.
type scripted struct {
	create  func(adapterpkg.Target) (adapterpkg.ResourceRef, error)
	destroy func(adapterpkg.Target) error
}

func (a *scripted) Create(_ context.Context, t adapterpkg.Target) (adapterpkg.ResourceRef, error) {
	if a.create == nil {
		return t.Ref, nil
	}
	return a.create(t)
}
func (a *scripted) Destroy(_ context.Context, t adapterpkg.Target) error {
	if a.destroy == nil {
		return nil
	}
	return a.destroy(t)
}

func scriptedManager(t *testing.T, a *scripted, prepare func(adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error)) *Manager {
	t.Helper()
	if prepare == nil {
		prepare = func(r adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error) {
			return adapterpkg.ResourceRef{Adapter: "slow", Adopted: r.Existing.Adopted, Path: r.Existing.Path}, nil
		}
	}
	slow := adapterpkg.Definition{Name: "slow", Adapter: a, CreateAllowed: true, VerifiesAdoption: true, CleanupOutsideLock: true,
		Prepare: func(_ context.Context, r adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error) {
			return prepare(r)
		},
		Validate: func(_ string, _ adapterpkg.Metadata, ref adapterpkg.ResourceRef) error {
			if ref.Adapter != "slow" || ref.Name == "bad" {
				return errors.New("invalid slow identity")
			}
			return nil
		},
		Env: func(adapterpkg.ResourceRef) adapterpkg.Environment { return adapterpkg.Environment{} },
	}
	return New(Options{Directory: t.TempDir(), Adapters: adapterpkg.NewRegistry(nonepkg.New(), slow), Owners: testManager().options.Owners})
}

func liveOwner(t *testing.T) ownerpkg.Reference {
	t.Helper()
	owner, err := bindOwner("pid:"+strconv.Itoa(os.Getpid()), time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func stored(t *testing.T, m *Manager, id string) (Lease, bool) {
	t.Helper()
	l, ok, err := m.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return l, ok
}

// lockFree proves the host-wide lock is not held by opening the store with a short deadline.
func lockFree(t *testing.T, m *Manager) error {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	s, err := m.openStore(ctx, m.options.Directory)
	if err != nil {
		return err
	}
	return s.Close()
}

func onlyID(t *testing.T, m *Manager) string {
	t.Helper()
	listing, err := m.List(context.Background(), Filter{})
	if err != nil || len(listing.Leases) != 1 {
		t.Fatalf("listing = %+v, %v", listing, err)
	}
	return listing.Leases[0].Lease.ID
}

func TestCreateRecordsWhatCreationLearned(t *testing.T) {
	a := &scripted{create: func(t adapterpkg.Target) (adapterpkg.ResourceRef, error) {
		ref := t.Ref
		ref.Name, ref.Claim = "slot-1", "tok-A"
		return ref, nil
	}}
	m := scriptedManager(t, a, nil)
	l, err := m.Create(context.Background(), CreateRequest{Owner: liveOwner(t), TTL: time.Hour, Adapter: "slow"})
	if err != nil || l.ResourceRef.Claim != "tok-A" {
		t.Fatalf("lease = %+v, %v", l, err)
	}
	if got, _ := stored(t, m, l.ID); got.ResourceRef.Name != "slot-1" || got.ResourceRef.Claim != "tok-A" {
		t.Fatalf("identity not durable: %+v", got.ResourceRef)
	}
}

func TestCreateFailureKeepsIntentAndLearnedIdentity(t *testing.T) {
	a := &scripted{create: func(t adapterpkg.Target) (adapterpkg.ResourceRef, error) {
		ref := t.Ref
		ref.Claim = "tok-A"
		return ref, errors.New("half created")
	}}
	m := scriptedManager(t, a, nil)
	if _, err := m.Create(context.Background(), CreateRequest{Owner: liveOwner(t), TTL: time.Hour, Adapter: "slow"}); err == nil {
		t.Fatal("create succeeded")
	}
	if got, _ := stored(t, m, onlyID(t, m)); got.ResourceRef.Claim != "tok-A" {
		t.Fatalf("lost the partial identity: %+v", got.ResourceRef)
	}
}

func TestCreateRefusesInvalidLearnedIdentity(t *testing.T) {
	a := &scripted{create: func(t adapterpkg.Target) (adapterpkg.ResourceRef, error) {
		ref := t.Ref
		ref.Name = "bad"
		return ref, nil
	}}
	m := scriptedManager(t, a, nil)
	if _, err := m.Create(context.Background(), CreateRequest{Owner: liveOwner(t), TTL: time.Hour, Adapter: "slow"}); err == nil {
		t.Fatal("accepted an invalid identity")
	}
	if got, _ := stored(t, m, onlyID(t, m)); got.ResourceRef.Name != "" {
		t.Fatalf("recorded an invalid identity: %+v", got.ResourceRef)
	}
}

func TestReleaseCleansUpOutsideLockAndForgetsAfter(t *testing.T) {
	a := &scripted{}
	m := scriptedManager(t, a, nil)
	l, err := m.Create(context.Background(), CreateRequest{Owner: liveOwner(t), TTL: time.Hour, Adapter: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	var discarded bool
	a.destroy = func(target adapterpkg.Target) error {
		discarded = target.DiscardUnlanded
		if _, still := stored(t, m, l.ID); !still {
			t.Error("forgot the lease before cleanup finished")
		}
		return lockFree(t, m)
	}
	r, err := m.Release(context.Background(), ReleaseRequest{ID: l.ID, Confirm: true, DiscardUnlanded: true})
	if err != nil || !r.Released || !discarded {
		t.Fatalf("release = %+v, %v, discard = %v", r, err, discarded)
	}
	if _, still := stored(t, m, l.ID); still {
		t.Fatal("lease survived a verified cleanup")
	}
}

func TestReleaseOutsideLockRetainsOnFailureOrChange(t *testing.T) {
	for name, tc := range map[string]struct {
		destroy      func(*Manager, string) error
		wantErr      bool
		wantRetained bool
	}{
		"destroy failed": {destroy: func(*Manager, string) error { return errors.New("unlanded") }, wantErr: true, wantRetained: true},
		"identity changed meanwhile": {destroy: func(m *Manager, id string) error {
			s, err := m.openStore(context.Background(), m.options.Directory)
			if err != nil {
				return err
			}
			defer s.Close()
			l := s.Leases[id]
			l.ResourceRef.Claim = "tok-B"
			s.Leases[id] = l
			return s.Save()
		}, wantErr: true, wantRetained: true},
		"forgotten meanwhile": {destroy: func(m *Manager, id string) error {
			s, err := m.openStore(context.Background(), m.options.Directory)
			if err != nil {
				return err
			}
			defer s.Close()
			delete(s.Leases, id)
			return s.Save()
		}},
	} {
		t.Run(name, func(t *testing.T) {
			a := &scripted{}
			m := scriptedManager(t, a, nil)
			l, err := m.Create(context.Background(), CreateRequest{Owner: liveOwner(t), TTL: time.Hour, Adapter: "slow"})
			if err != nil {
				t.Fatal(err)
			}
			a.destroy = func(adapterpkg.Target) error { return tc.destroy(m, l.ID) }
			r, err := m.Release(context.Background(), ReleaseRequest{ID: l.ID, Confirm: true})
			if (err != nil) != tc.wantErr || r.Released == tc.wantErr {
				t.Fatalf("release = %+v, %v", r, err)
			}
			if _, still := stored(t, m, l.ID); still != tc.wantRetained {
				t.Fatalf("retained = %v", still)
			}
		})
	}
}

func TestReleasePreviewNeverCleansUp(t *testing.T) {
	a := &scripted{destroy: func(adapterpkg.Target) error { return errors.New("must not run") }}
	m := scriptedManager(t, a, nil)
	l, err := m.Create(context.Background(), CreateRequest{Owner: liveOwner(t), TTL: time.Hour, Adapter: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := m.Release(context.Background(), ReleaseRequest{ID: l.ID}); err != nil || r.Released || !r.Exists {
		t.Fatalf("preview = %+v, %v", r, err)
	}
}

func TestSweepReclaimsOutsideLockWithoutDiscarding(t *testing.T) {
	a := &scripted{}
	m := scriptedManager(t, a, nil)
	owner := liveOwner(t)
	var ids []string
	for _, adapter := range []string{"slow", "slow", "none"} {
		l, err := m.Create(context.Background(), CreateRequest{Owner: owner, TTL: time.Hour, Adapter: adapter})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, l.ID)
	}
	// Expire all three, so only the backstop makes them eligible.
	s, err := m.openStore(context.Background(), m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	for id, l := range s.Leases {
		l.Expires = time.Now().UTC().Add(-time.Minute)
		s.Leases[id] = l
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	failing := ids[1]
	a.destroy = func(target adapterpkg.Target) error {
		if target.DiscardUnlanded {
			t.Error("sweep asked to discard unlanded work")
		}
		if err := lockFree(t, m); err != nil {
			return err
		}
		if target.Metadata.LeaseID == failing {
			return errors.New("unlanded work")
		}
		return nil
	}
	r, err := m.Sweep(context.Background(), SweepRequest{Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ids[0], ids[2]}
	if want[0] > want[1] {
		want[0], want[1] = want[1], want[0]
	}
	if !reflect.DeepEqual(r.Reclaimed, want) || r.Failures[failing] != "unlanded work" || len(r.Eligible) != 3 {
		t.Fatalf("sweep = %+v", r)
	}
	if _, still := stored(t, m, failing); !still {
		t.Fatal("failed cleanup lost its lease")
	}
}

func TestAdoptionVerifiedByAdapterSeesRelatedLeases(t *testing.T) {
	var seen []adapterpkg.ResourceRef
	m := scriptedManager(t, &scripted{}, func(r adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error) {
		seen = r.Leased
		if !r.Existing.Adopted {
			return adapterpkg.ResourceRef{Adapter: "slow", Path: "/created"}, nil
		}
		if r.Existing.Path == "/taken" {
			return adapterpkg.ResourceRef{}, errors.New("slot is already under a lease")
		}
		return adapterpkg.ResourceRef{Adapter: "slow", Adopted: true, Path: r.Existing.Path, ExcludeRemotes: r.ExcludeRemotes}, nil
	})
	owner := liveOwner(t)
	if _, err := m.Create(context.Background(), CreateRequest{Owner: owner, TTL: time.Hour, Adapter: "slow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(context.Background(), CreateRequest{Owner: owner, TTL: time.Hour, Adapter: "none"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Adopt(context.Background(), AdoptRequest{Owner: owner, TTL: time.Hour, Resource: adapterpkg.ResourceRef{Adapter: "slow", Path: "/taken"}}); err == nil {
		t.Fatal("adapter refusal ignored")
	}
	l, err := m.Adopt(context.Background(), AdoptRequest{Owner: owner, TTL: time.Hour, Resource: adapterpkg.ResourceRef{Adapter: "slow", Path: "/slot"}, ExcludeRemotes: []string{"gate"}})
	if err != nil || !l.ResourceRef.Adopted || l.ResourceRef.ExcludeRemotes[0] != "gate" || l.Labels["io.dibs.lease"] != l.ID {
		t.Fatalf("adopt = %+v, %v", l, err)
	}
	if len(seen) != 1 || seen[0].Path != "/created" {
		t.Fatalf("related view = %+v", seen)
	}
}
