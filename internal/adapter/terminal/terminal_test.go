package terminal

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

const pinnedStart = "Wed Nov 11 10:00:00 2026"

// world is a fake tool and process table. Signals and closes mutate it the way a real terminal would.
type world struct {
	panes       map[string]int
	starts      map[int]string
	childPIDs   []int
	unknown     bool
	listErr     error
	signals     []string
	closes      []string
	hupExits    bool
	closeKills  bool
	closeErr    error
	closeLeaves bool
}

func (w *world) system() System {
	return System{
		Process: func(pid int) (string, ownerpkg.Liveness) {
			if w.unknown {
				return "", ownerpkg.Unknown
			}
			if start, ok := w.starts[pid]; ok {
				return start, ownerpkg.Alive
			}
			return "", ownerpkg.Dead
		},
		Signal: func(pid int, sig syscall.Signal) error {
			w.signals = append(w.signals, sig.String())
			if sig == syscall.SIGHUP && w.hupExits {
				delete(w.starts, pid)
				for id, p := range w.panes {
					if p == pid {
						delete(w.panes, id)
					}
				}
			}
			return nil
		},
		Sleep: func(time.Duration) {},
		Grace: 300 * time.Millisecond,
	}
}

func (w *world) driver() Driver {
	return Driver{Name: "fake", Noun: "pane", ValidID: func(id string) bool { return strings.HasPrefix(id, "%") },
		ResolveServer: func(context.Context, System, string) (string, error) { return "/sock", nil },
		List: func(context.Context, System, string) ([]Pane, error) {
			if w.listErr != nil {
				return nil, w.listErr
			}
			panes := []Pane{}
			for id, pid := range w.panes {
				panes = append(panes, Pane{ID: id, PID: pid, ChildPIDs: w.childPIDs})
			}
			return panes, nil
		},
		Close: func(_ context.Context, _ System, _ string, p Pane) error {
			w.closes = append(w.closes, p.ID)
			if !w.closeLeaves {
				delete(w.panes, p.ID)
				if w.closeKills {
					delete(w.starts, p.PID)
				}
			}
			return w.closeErr
		},
	}
}

func target(id string) adapterpkg.Target {
	return adapterpkg.Target{Ref: adapterpkg.ResourceRef{Adopted: true, Adapter: "fake", Name: id, Terminal: &adapterpkg.TerminalPin{Server: "/sock", PID: 100, Start: pinnedStart}}}
}

func TestPreparePinsHeldShellIdentity(t *testing.T) {
	w := world{panes: map[string]int{"%1": 100}, starts: map[int]string{100: pinnedStart, 201: "shell-start"}, childPIDs: []int{201}}
	d := New(w.driver(), w.system())
	existing := adapterpkg.ResourceRef{Adapter: "fake", Name: "%1", Adopted: true, Terminal: &adapterpkg.TerminalPin{Server: "/sock"}}
	got, err := d.Prepare(context.Background(), adapterpkg.PrepareRequest{Existing: existing})
	if err != nil || !reflect.DeepEqual(got.Terminal.Children, []adapterpkg.TerminalChild{{PID: 201, Start: "shell-start"}}) {
		t.Fatalf("prepared child identity = %+v, %v", got.Terminal, err)
	}
}

func TestDestroyDecisionTable(t *testing.T) {
	cases := []struct {
		name    string
		world   world
		wantErr string
		signals []string
		closes  []string
	}{
		{name: "owned shell exits on hangup, no close needed", world: world{panes: map[string]int{"%1": 100}, starts: map[int]string{100: pinnedStart}, hupExits: true}, signals: []string{"hangup"}},
		{name: "owned shell ignores hangup, pane closed", world: world{panes: map[string]int{"%1": 100}, starts: map[int]string{100: pinnedStart}, closeKills: true}, signals: []string{"hangup"}, closes: []string{"%1"}},
		{name: "already absent", world: world{panes: map[string]int{}, starts: map[int]string{}}},
		{name: "ID reused by another shell after ours died", world: world{panes: map[string]int{"%1": 200}, starts: map[int]string{200: pinnedStart}}},
		{name: "PID reused elsewhere and pane gone", world: world{panes: map[string]int{}, starts: map[int]string{100: "Thu Nov 12 10:00:00 2026"}}},
		{name: "dead shell left in a remain-on-exit pane", world: world{panes: map[string]int{"%1": 100}, starts: map[int]string{}}, closes: []string{"%1"}},
		{name: "no server running proves absence", world: world{listErr: ErrNoServer, starts: map[int]string{}}},
		{name: "close reports an error but the pane is gone", world: world{panes: map[string]int{"%1": 100}, starts: map[int]string{}, closeErr: errors.New("already closing")}, closes: []string{"%1"}},
		{name: "pinned shell alive but pane gone", world: world{panes: map[string]int{}, starts: map[int]string{100: pinnedStart}}, wantErr: "no longer carries it"},
		{name: "pinned shell alive but ID carries another shell", world: world{panes: map[string]int{"%1": 200}, starts: map[int]string{100: pinnedStart, 200: pinnedStart}}, wantErr: "no longer carries it"},
		{name: "pane carries a reused PID", world: world{panes: map[string]int{"%1": 100}, starts: map[int]string{100: "Thu Nov 12 10:00:00 2026"}}, wantErr: "reused pid"},
		{name: "process table unreadable", world: world{panes: map[string]int{"%1": 100}, unknown: true}, wantErr: "cannot inspect"},
		{name: "tool failure", world: world{listErr: errors.New("timeout"), starts: map[int]string{}}, wantErr: "cannot list"},
		{name: "server down while pinned shell lives", world: world{listErr: ErrNoServer, starts: map[int]string{100: pinnedStart}}, wantErr: "no longer carries it"},
		{name: "pane survives close", world: world{panes: map[string]int{"%1": 100}, starts: map[int]string{100: pinnedStart}, closeLeaves: true}, wantErr: "survived cleanup", signals: []string{"hangup"}, closes: []string{"%1"}},
		{name: "close fails and pane remains", world: world{panes: map[string]int{"%1": 100}, starts: map[int]string{}, closeErr: errors.New("denied"), closeLeaves: true}, wantErr: "cannot close", closes: []string{"%1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.world
			if w.panes == nil {
				w.panes = map[string]int{}
			}
			err := resource{w.driver(), w.system()}.Destroy(context.Background(), target("%1"))
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Destroy error = %v, want %q", err, tc.wantErr)
			}
			if !reflect.DeepEqual(w.signals, tc.signals) || !reflect.DeepEqual(w.closes, tc.closes) {
				t.Fatalf("signals %v closes %v, want %v %v", w.signals, w.closes, tc.signals, tc.closes)
			}
		})
	}
}

func TestCloseErrorStillVerifiesHeldShell(t *testing.T) {
	w := world{
		panes:     map[string]int{"%1": 100},
		starts:    map[int]string{100: pinnedStart, 201: "shell-start"},
		childPIDs: []int{201}, closeKills: true, closeErr: errors.New("close failed"),
	}
	pinned := target("%1")
	pinned.Ref.Terminal.Children = []adapterpkg.TerminalChild{{PID: 201, Start: "shell-start"}}
	if err := (resource{w.driver(), w.system()}).Destroy(context.Background(), pinned); err == nil || !strings.Contains(err.Error(), "1 held child process(es) survived cleanup") {
		t.Fatalf("Destroy error = %v, want surviving shell to retain lease", err)
	}
}

func TestUnpinnedChildRefusesBeforeAnyStep(t *testing.T) {
	// Destroy cannot record a child it discovers, so a retry could not verify it; refuse before touching anything.
	w := world{panes: map[string]int{"%1": 100}, starts: map[int]string{100: pinnedStart, 201: "late-start"}, childPIDs: []int{201}, closeKills: true}
	err := (resource{w.driver(), w.system()}).Destroy(context.Background(), target("%1"))
	if err == nil || !strings.Contains(err.Error(), "unpinned child 201") || len(w.signals) != 0 || len(w.closes) != 0 {
		t.Fatalf("unpinned child must retain the lease untouched: %v signals=%v closes=%v", err, w.signals, w.closes)
	}
}

func TestDeadPinnedPIDWithAnotherStartIsNotOurs(t *testing.T) {
	// A dead process at the pinned PID with a different start (a reused PID, now a zombie) must not let cleanup close the pane.
	w := world{panes: map[string]int{"%1": 100}, starts: map[int]string{}, closeKills: true}
	sys := w.system()
	sys.Process = func(pid int) (string, ownerpkg.Liveness) { return "other-start", ownerpkg.Dead }
	if err := (resource{w.driver(), sys}).Destroy(context.Background(), target("%1")); err == nil || !strings.Contains(err.Error(), "reused pid") || len(w.closes) != 0 {
		t.Fatalf("reused dead pid must retain the lease: %v closes=%v", err, w.closes)
	}
}

func TestListedTerminalWithoutProcess(t *testing.T) {
	for _, stable := range []bool{false, true} {
		w := world{panes: map[string]int{"%1": 0}, starts: map[int]string{}}
		d := w.driver()
		d.StableIDs = stable
		err := (resource{d, w.system()}).Destroy(context.Background(), target("%1"))
		switch {
		case stable && (err != nil || !reflect.DeepEqual(w.closes, []string{"%1"})):
			t.Fatalf("a never-reused ID listed without a process is ours and must be closed: %v %v", err, w.closes)
		case !stable && (err == nil || !strings.Contains(err.Error(), "without a process") || len(w.closes) != 0):
			t.Fatalf("a reusable ID listed without a process must retain the lease untouched: %v %v", err, w.closes)
		}
	}
}

func TestPinnedIdentityRecheckedBeforeHangup(t *testing.T) {
	w := world{panes: map[string]int{"%1": 100}, starts: map[int]string{100: pinnedStart}, hupExits: true}
	sys := w.system()
	reads := 0
	inner := sys.Process
	sys.Process = func(pid int) (string, ownerpkg.Liveness) {
		if reads++; pid == 100 && reads > 1 {
			return "reused-start", ownerpkg.Alive // the PID was reused after observe sampled it
		}
		return inner(pid)
	}
	if err := (resource{w.driver(), sys}).Destroy(context.Background(), target("%1")); err == nil || !strings.Contains(err.Error(), "changed before hangup") || len(w.signals) != 0 {
		t.Fatalf("a PID reused before the hangup must not be signalled: %v %v", err, w.signals)
	}
}

func TestDestroyClosesWhenHangupIsNotPermitted(t *testing.T) {
	w := world{panes: map[string]int{"%1": 100}, starts: map[int]string{100: pinnedStart}, closeKills: true}
	sys := w.system()
	sys.Signal = func(int, syscall.Signal) error { return syscall.EPERM }
	if err := (resource{w.driver(), sys}).Destroy(context.Background(), target("%1")); err != nil || !reflect.DeepEqual(w.closes, []string{"%1"}) {
		t.Fatalf("root-owned child must be hung up by the close: %v %v", err, w.closes)
	}
}

func TestPrepareValidateAndDescribe(t *testing.T) {
	w := world{panes: map[string]int{"%1": 100, "%2": 0, "%3": 300}, starts: map[int]string{100: pinnedStart}}
	d := New(w.driver(), w.system())
	existing := adapterpkg.ResourceRef{Adopted: true, Adapter: "fake", Name: "%1", Terminal: &adapterpkg.TerminalPin{Server: "/sock"}}
	ref, err := d.Prepare(context.Background(), adapterpkg.PrepareRequest{Existing: existing})
	if err != nil || !reflect.DeepEqual(ref, target("%1").Ref) {
		t.Fatalf("prepare must pin server, pid, and start: %+v %v", ref, err)
	}
	if err := d.Validate("", adapterpkg.Metadata{}, ref); err != nil {
		t.Fatal(err)
	}
	if got := d.Terminal.Describe(ref); got != "fake pane %1 on /sock (pid 100 started "+pinnedStart+")" {
		t.Fatalf("describe: %q", got)
	}
	for name, ref := range map[string]adapterpkg.ResourceRef{
		"no process":       {Adopted: true, Name: "%2"},
		"dead process":     {Adopted: true, Name: "%3"},
		"missing terminal": {Adopted: true, Name: "%9"},
		"not adopted":      {Name: "%1"},
		"invalid ID":       {Adopted: true, Name: "1"},
	} {
		if _, err := d.Prepare(context.Background(), adapterpkg.PrepareRequest{Existing: ref}); err == nil {
			t.Fatalf("%s: adoption accepted", name)
		}
	}
	for name, mutate := range map[string]func(*adapterpkg.ResourceRef){
		"not adopted": func(r *adapterpkg.ResourceRef) { r.Adopted = false },
		"no pin":      func(r *adapterpkg.ResourceRef) { r.Terminal = nil },
		"no start":    func(r *adapterpkg.ResourceRef) { r.Terminal.Start = "" },
		"invalid child": func(r *adapterpkg.ResourceRef) {
			r.Terminal.Children = []adapterpkg.TerminalChild{{PID: 0, Start: "shell-start"}}
		},
		"duplicate child": func(r *adapterpkg.ResourceRef) {
			r.Terminal.Children = []adapterpkg.TerminalChild{{PID: 201, Start: "a"}, {PID: 201, Start: "b"}}
		},
		"no server":     func(r *adapterpkg.ResourceRef) { r.Terminal.Server = "" },
		"docker scope":  func(r *adapterpkg.ResourceRef) { r.Context = "orbstack" },
		"snapshot file": func(r *adapterpkg.ResourceRef) { r.File = "/tmp/x" },
	} {
		bad := target("%1").Ref
		mutate(&bad)
		if err := d.Validate("", adapterpkg.Metadata{}, bad); err == nil {
			t.Fatalf("%s: invalid terminal record accepted", name)
		}
	}
	if _, err := d.Adapter.Create(context.Background(), target("%1")); err == nil {
		t.Fatal("terminals must not be created by dibs")
	}
	rows, err := d.Discover(context.Background(), adapterpkg.ResourceRef{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("discover: %v %v", rows, err)
	}
}

func TestMergeEnvRemovesEmptyOverrides(t *testing.T) {
	got := mergeEnv([]string{"A=1", "HERDR_SESSION=default", "B=2"}, []string{"HERDR_SESSION=", "C=3"})
	if !reflect.DeepEqual(got, []string{"A=1", "B=2", "C=3"}) {
		t.Fatalf("env: %v", got)
	}
}

func TestUnpinnedChildRefusesBeforeCloseOnEveryPath(t *testing.T) {
	// A leftover terminal (its pinned shell is gone) still carrying an unpinned live child.
	w := world{panes: map[string]int{"%1": 100}, starts: map[int]string{301: "late"}, childPIDs: []int{301}, closeKills: true}
	if err := (resource{w.driver(), w.system()}).Destroy(context.Background(), target("%1")); err == nil || !strings.Contains(err.Error(), "unpinned child 301") || len(w.closes) != 0 {
		t.Fatalf("leftover with an unpinned child must retain the lease untouched: %v closes=%v", err, w.closes)
	}
	// A child that appears in the post-hangup listing.
	w = world{panes: map[string]int{"%1": 100}, starts: map[int]string{100: pinnedStart}, closeKills: true}
	sys := w.system()
	signal := sys.Signal
	sys.Signal = func(pid int, sig syscall.Signal) error {
		w.childPIDs, w.starts[302] = []int{302}, "spawned-after-hangup"
		return signal(pid, sig)
	}
	if err := (resource{w.driver(), sys}).Destroy(context.Background(), target("%1")); err == nil || !strings.Contains(err.Error(), "unpinned child 302") || len(w.closes) != 0 {
		t.Fatalf("a child seen only after the hangup must stop the close: %v closes=%v", err, w.closes)
	}
}

func TestSettleRidesOutATransientReadDuringTeardown(t *testing.T) {
	w := world{panes: map[string]int{"%1": 100}, starts: map[int]string{100: pinnedStart}}
	d := w.driver()
	hungUp, glitches := false, 0
	list := d.List
	d.List = func(ctx context.Context, sys System, server string) ([]Pane, error) {
		if hungUp && glitches == 0 {
			// The holder is exiting: alive, but no longer listed as a terminal.
			glitches++
			return []Pane{}, nil
		}
		if hungUp {
			delete(w.panes, "%1")
			delete(w.starts, 100)
		}
		return list(ctx, sys, server)
	}
	sys := w.system()
	sys.Signal = func(int, syscall.Signal) error { hungUp = true; return nil }
	if err := (resource{d, sys}).Destroy(context.Background(), target("%1")); err != nil || glitches != 1 || len(w.closes) != 0 {
		t.Fatalf("a transient read during teardown must not fail cleanup: %v glitches=%d closes=%v", err, glitches, w.closes)
	}
}
