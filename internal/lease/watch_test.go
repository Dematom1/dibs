package lease

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	policypkg "github.com/Dematom1/dibs/internal/policy"
)

// watched starts an owner process, leases a none resource to it, and runs its watcher.
func watched(t *testing.T, ttl time.Duration, p *policypkg.Policy) (*Manager, Lease, *exec.Cmd, <-chan SweepResult) {
	t.Helper()
	m, l, owner := ownedLease(t, ttl, p)
	done := make(chan SweepResult, 1)
	go func() {
		r, err := m.Watch(context.Background(), l.ID)
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	return m, l, owner, done
}

func ownedLease(t *testing.T, ttl time.Duration, p *policypkg.Policy) (*Manager, Lease, *exec.Cmd) {
	t.Helper()
	interval := watchInterval
	watchInterval = 20 * time.Millisecond
	t.Cleanup(func() { watchInterval = interval })
	owner := exec.Command("sleep", "60")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Process.Kill(); owner.Wait() })
	m := New(Options{Directory: t.TempDir(), Adapters: testAdapters(), Owners: testManager().options.Owners})
	ref, err := bindOwner("pid:"+strconv.Itoa(owner.Process.Pid), 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	l, _, err := m.Create(context.Background(), CreateRequest{Owner: ref, TTL: ttl, Adapter: "none", Policy: p})
	if err != nil {
		t.Fatal(err)
	}
	return m, l, owner
}

func waitWatcher(t *testing.T, done <-chan SweepResult) SweepResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not exit")
		return SweepResult{}
	}
}

func exists(t *testing.T, m *Manager, id string) bool {
	t.Helper()
	_, ok, err := m.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestWatcherReclaimsOnOwnerExit(t *testing.T) {
	m, l, owner, done := watched(t, time.Hour, nil)
	owner.Process.Kill()
	owner.Wait()
	if r := waitWatcher(t, done); len(r.Reclaimed) != 1 || r.Reclaimed[0] != l.ID {
		t.Fatalf("watcher result = %+v", r)
	}
	if exists(t, m, l.ID) {
		t.Fatal("lease survived its owner's exit")
	}
}

func TestWatcherHonorsReportPolicy(t *testing.T) {
	report := &policypkg.Policy{Row: "none", Binding: policypkg.Owner, TTL: time.Hour, Action: policypkg.Report}
	m, l, owner, done := watched(t, time.Hour, report)
	owner.Process.Kill()
	owner.Wait()
	if r := waitWatcher(t, done); len(r.Reclaimed) != 0 || r.Held[l.ID] != HoldReportOnly {
		t.Fatalf("watcher result = %+v", r)
	}
	if !exists(t, m, l.ID) {
		t.Fatal("a report policy lease was reclaimed")
	}
}

func TestWatcherIgnoresExpiryAndExitsOnRelease(t *testing.T) {
	m, l, _, done := watched(t, time.Millisecond, nil)
	time.Sleep(10 * watchInterval)
	select {
	case r := <-done:
		t.Fatalf("watcher acted on an expired lease with a live owner: %+v", r)
	default:
	}
	if !exists(t, m, l.ID) {
		t.Fatal("expiry reclaimed the lease without a sweep")
	}
	if _, err := m.Release(context.Background(), ReleaseRequest{ID: l.ID, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if r := waitWatcher(t, done); len(r.Eligible) != 0 {
		t.Fatalf("watcher result after release = %+v", r)
	}
}

func TestWatcherIsSingle(t *testing.T) {
	m, l, _ := ownedLease(t, time.Hour, nil)
	dir := filepath.Join(m.options.Directory, "watch")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, l.ID+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Watch(context.Background(), l.ID); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second watcher for a held lease kept running")
	}
}

// An exit event whose owner check stays unknown (an unreaped zombie whose ps
// read fails) must not spin: the watcher waits an interval before looking again.
func TestWatcherThrottlesInconclusiveExit(t *testing.T) {
	interval := watchInterval
	watchInterval = 20 * time.Millisecond
	t.Cleanup(func() { watchInterval = interval })
	zombie := exec.Command("true")
	if err := zombie.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { zombie.Wait() })
	var checks atomic.Int64
	var started atomic.Bool
	pid := ownerpkg.Definition{Name: "pid",
		Bind: func(context.Context, string, time.Duration, time.Time) (ownerpkg.Reference, error) {
			return ownerpkg.Reference{}, errors.New("unused")
		},
		Check: func(_ context.Context, r ownerpkg.Reference, _ time.Time) ownerpkg.Observation {
			if !started.Load() {
				return ownerpkg.Observation{Liveness: ownerpkg.Alive, Identity: r.Identity}
			}
			checks.Add(1)
			return ownerpkg.Observation{Liveness: ownerpkg.Unknown}
		}}
	m := New(Options{Directory: t.TempDir(), Adapters: testAdapters(), Owners: ownerpkg.NewRegistry(pid)})
	owner := ownerpkg.Reference{Kind: "pid", ID: strconv.Itoa(zombie.Process.Pid), Identity: "start"}
	l, _, err := m.Create(context.Background(), CreateRequest{Owner: owner, TTL: time.Hour, Adapter: "none"})
	if err != nil {
		t.Fatal(err)
	}
	started.Store(true)
	done := make(chan error, 1)
	go func() { _, err := m.Watch(context.Background(), l.ID); done <- err }()
	time.Sleep(10 * watchInterval)
	if _, err := m.Release(context.Background(), ReleaseRequest{ID: l.ID, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// At most one immediate look after each exit event, plus one per interval.
	if n := checks.Load(); n > 30 {
		t.Fatalf("watcher checked an unknown owner %d times in 10 intervals", n)
	}
}
