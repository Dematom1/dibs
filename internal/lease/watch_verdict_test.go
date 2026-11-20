package lease

import (
	"context"
	"os/exec"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

// A watched resource whose owner is dead is still held while a live process
// works in a watched directory, and reclaimed once that work is gone.
func TestWatchedLeaseHeldWhileLiveWork(t *testing.T) {
	slot := t.TempDir()
	l := testLease(t)
	l.ResourceRef = adapterpkg.ResourceRef{Adapter: "none", Watch: []string{slot}}
	dead := func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Dead }
	worker := exec.Command("sleep", "60")
	worker.Dir = slot
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	idleAdapter := observed(adapterpkg.Activity{IdleKnown: true}, nil)
	v := decide(context.Background(), l, l.Machine, testNow, dead, idleAdapter)
	worker.Process.Kill()
	worker.Wait()
	if v.Reclaim || v.Hold != HoldResourceBusy {
		t.Fatalf("live work in the watched slot must hold the lease: %+v", v)
	}
	if v := decide(context.Background(), l, l.Machine, testNow, dead, idleAdapter); !v.Reclaim {
		t.Fatalf("idle watched slot must reclaim a dead-owned lease: %+v", v)
	}
}

func TestWatchedResourceNeedsVersionNine(t *testing.T) {
	s := testStore(t)
	l := testLease(t)
	l.ResourceRef.Watch = []string{"/slot"}
	s.Leases[l.ID] = l
	s.Version = 8
	if s.validate() == nil {
		t.Fatal("a version 8 ledger carried a watched resource")
	}
	s.settleVersion()
	if s.Version != 9 || s.validate() != nil {
		t.Fatalf("watched resource saved as version %d", s.Version)
	}
	handoff := testLease(t)
	handoff.Handoff = &Handoff{From: ownerpkg.Reference{Kind: "pid", ID: "1"}, At: testNow}
	s.Leases[handoff.ID] = handoff
	if s.settleVersion(); s.Version != 9 {
		t.Fatalf("a handoff record lowered the version to %d", s.Version)
	}
}
