package lease

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	terminalpkg "github.com/Dematom1/dibs/internal/adapter/terminal"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

// fakeTerminals serves one live pane per server, each carrying this test process as its shell.
func fakeTerminals() adapterpkg.Definition {
	pid := os.Getpid()
	sys := terminalpkg.System{Process: ownerpkg.AnyProcessIdentity, Signal: func(int, syscall.Signal) error { return nil }, Sleep: func(time.Duration) {}}
	return terminalpkg.New(terminalpkg.Driver{Name: "fake", Noun: "pane", ValidID: func(id string) bool { return strings.HasPrefix(id, "%") },
		ResolveServer: func(_ context.Context, _ terminalpkg.System, s string) (string, error) { return s, nil },
		List: func(context.Context, terminalpkg.System, string) ([]terminalpkg.Pane, error) {
			return []terminalpkg.Pane{{ID: "%1", PID: pid}}, nil
		},
		Close: func(context.Context, terminalpkg.System, string, terminalpkg.Pane) error { return nil },
	}, sys)
}

func TestTerminalAdoptionIsScopedToItsServer(t *testing.T) {
	ctx := context.Background()
	m := testManager()
	m.options.Directory = t.TempDir()
	m.options.Adapters = adapterpkg.NewRegistry(fakeTerminals())
	o, err := bindOwner("pid:"+strconv.Itoa(os.Getpid()), time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pane := func(server string) adapterpkg.ResourceRef {
		return adapterpkg.ResourceRef{Adapter: "fake", Name: "%1", Terminal: &adapterpkg.TerminalPin{Server: server}}
	}
	d, _ := m.options.Adapters.Lookup("fake")
	found, err := d.Discover(ctx, pane("/a"))
	if err != nil || len(found) != 1 {
		t.Fatalf("discover: %v %v", found, err)
	}
	l, _, err := m.Adopt(ctx, AdoptRequest{Owner: o, TTL: time.Hour, Resource: pane("/a")})
	if err != nil {
		t.Fatal(err)
	}
	if pin := l.ResourceRef.Terminal; !l.ResourceRef.Adopted || pin == nil || pin.Server != "/a" || pin.PID != os.Getpid() || pin.Start == "" {
		t.Fatalf("adoption did not pin the terminal: %+v", l.ResourceRef)
	}
	if again, existing, err := m.Adopt(ctx, AdoptRequest{Owner: o, TTL: 2 * time.Hour, Resource: pane("/a")}); err != nil || !existing || again.ID != l.ID || !again.Expires.Equal(l.Expires) {
		t.Fatalf("a same-owner retry must return the existing lease unchanged: %+v %v %v", again, existing, err)
	}
	when := time.Now()
	l.Releasing = &when
	d, _ = m.options.Adapters.Lookup("fake")
	if _, ok := sameTerminalLease(ctx, &store{Leases: map[string]Lease{l.ID: l}}, d, AdoptRequest{Owner: o, Resource: pane("/a")}, when); ok {
		t.Fatal("retry returned a terminal lease whose cleanup is in progress")
	}
	other, err := bindOwner("pid:"+strconv.Itoa(os.Getppid()), time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Adopt(ctx, AdoptRequest{Owner: other, TTL: time.Hour, Resource: pane("/a")}); err == nil || !strings.Contains(err.Error(), "already under") {
		t.Fatalf("another owner took a leased terminal: %v", err)
	}
	if leftover, err := m.Unleased(ctx, pane("/a"), found); err != nil || len(leftover) != 0 {
		t.Fatalf("leased pane listed as orphan: %v %v", leftover, err)
	}
	if leftover, err := m.Unleased(ctx, pane("/b"), found); err != nil || len(leftover) != 1 {
		t.Fatalf("the same pane ID on another server is a different terminal: %v %v", leftover, err)
	}
	if _, _, err := m.Adopt(ctx, AdoptRequest{Owner: o, TTL: time.Hour, Resource: adapterpkg.ResourceRef{Adapter: "fake", Name: "%1"}}); err == nil {
		t.Fatal("terminal adoption without a server accepted")
	}
	keyed, _, err := m.Adopt(ctx, AdoptRequest{Owner: o, TTL: time.Hour, Resource: pane("/b"), Key: "task-7:pane"})
	if err != nil {
		t.Fatal(err)
	}
	if again, done, err := m.Adopt(ctx, AdoptRequest{Owner: o, TTL: time.Hour, Resource: pane("/b"), Key: "task-7:pane"}); err != nil || !done || again.ID != keyed.ID {
		t.Fatalf("a keyed retry must return the keyed lease: %+v %v %v", again, done, err)
	}
	if _, _, err := m.Adopt(ctx, AdoptRequest{Owner: o, TTL: time.Hour, Resource: pane("/c"), Key: "task-7:pane"}); err == nil || !strings.Contains(err.Error(), "different request") {
		t.Fatalf("a key reused for the same pane ID on another server must conflict: %v", err)
	}
	s, err := m.openStore(ctx, m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Version != 6 {
		t.Fatalf("terminal leases must save as state version 6 so a version 5 reader refuses them, got %d", s.Version)
	}
	delete(s.Leases, keyed.ID)
	alive := func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Alive }
	unknown := func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Unknown }
	dead := func(ownerpkg.Reference, time.Time) ownerpkg.Liveness { return ownerpkg.Dead }
	for _, check := range []func(ownerpkg.Reference, time.Time) ownerpkg.Liveness{alive, unknown} {
		// Closing a terminal ends its shell, so expiry alone never reclaims it.
		for _, at := range []time.Time{time.Now(), l.Expires.Add(time.Hour)} {
			if r, _ := sweep(ctx, s, l.Machine, at, false, check, m.options.Adapters); len(r.Eligible) != 0 || r.Targets != nil {
				t.Fatalf("a live or unknown owner's terminal was offered for reclaim: %+v", r)
			}
		}
	}
	r, _ := sweep(ctx, s, l.Machine, time.Now(), false, dead, m.options.Adapters)
	if len(r.Eligible) != 1 || !strings.HasPrefix(r.Targets[l.ID], "fake pane %1 on /a (pid "+strconv.Itoa(os.Getpid())) || len(s.Leases) != 1 {
		t.Fatalf("dry run must name exactly what it would close and change nothing: %+v", r)
	}
}
