package treehousesubshell

import (
	"context"
	"errors"
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"

	terminalpkg "github.com/Dematom1/dibs/internal/adapter/terminal"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	"time"
)

type reply struct {
	out, stderr string
	code        int
	err         error
}

const table = `  100     1 /usr/bin/login -flp user /bin/bash
  200   100 /nix/store/abc-treehouse/bin/treehouse get
  203   200 /usr/bin/treehouse-helper --work
  201   200 /bin/zsh -i -l
  202   201 claude
  300   100 treehouse enter wt-3
  301   300 /bin/zsh -i -l
  400   100 treehouse status
`

func fake(t *testing.T, replies map[string]reply, signals *[]string) terminalpkg.System {
	return terminalpkg.System{
		Run: func(_ context.Context, env []string, name string, args ...string) ([]byte, string, int, error) {
			if name == "lsof" {
				r := replies["lsof "+strings.Join(args, " ")]
				return []byte(r.out), r.stderr, r.code, r.err
			}
			if name != "ps" || !reflect.DeepEqual(env, []string{"LC_ALL=C"}) {
				t.Fatalf("unexpected command %s %v", name, env)
			}
			r, ok := replies[strings.Join(args, " ")]
			if !ok {
				t.Fatalf("unexpected ps call %v", args)
			}
			return []byte(r.out), r.stderr, r.code, r.err
		},
		Signal: func(pid int, sig syscall.Signal) error {
			*signals = append(*signals, sig.String()+":"+strconv.Itoa(pid))
			return nil
		},
	}
}

const all = "-ax -ww -o pid=,ppid=,command="

func TestListLookupHangupClose(t *testing.T) {
	var signals []string
	sys := fake(t, map[string]reply{
		all:                                  {out: table},
		"lsof -a -d cwd -Fpn -p 203,201,301": {out: "p203\nfcwd\nn/pool/1/helper\np201\nfcwd\nn/pool/1/repo\np301\nfcwd\nn/pool/3/repo\n"},
	}, &signals)
	panes, err := list(context.Background(), sys, local)
	want := []terminalpkg.Pane{{ID: "200", PID: 200, Label: "/nix/store/abc-treehouse/bin/treehouse get", Command: "/usr/bin/treehouse-helper --work, /bin/zsh -i -l", Dir: "/pool/1/helper, /pool/1/repo", ChildPIDs: []int{203, 201}}, {ID: "300", PID: 300, Label: "treehouse enter wt-3", Command: "/bin/zsh -i -l", Dir: "/pool/3/repo", ChildPIDs: []int{301}}}
	if err != nil || !reflect.DeepEqual(panes, want) {
		t.Fatalf("panes %+v %v", panes, err)
	}
	if p, found, err := lookup(context.Background(), sys, local, "200"); err != nil || !found || p.PID != 200 || !reflect.DeepEqual(p.ChildPIDs, []int{203, 201}) {
		t.Fatalf("live subshell holder: %+v %v %v", p, found, err)
	}
	if _, found, err := lookup(context.Background(), sys, local, "202"); err != nil || found {
		t.Fatalf("a reused PID that is not treehouse must read as absent: %v %v", found, err)
	}
	if _, found, err := lookup(context.Background(), sys, local, "999"); err != nil || found {
		t.Fatalf("exited process: %v %v", found, err)
	}
	hangup(context.Background(), sys, terminalpkg.Pane{ID: "200", PID: 200, ChildPIDs: []int{203, 201}})
	if err := closeSubshell(context.Background(), sys, local, terminalpkg.Pane{ID: "200", PID: 200}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(signals, []string{"hangup:203", "hangup:201", "terminated:200"}) {
		t.Fatalf("hangup must reach only the held subshell, then treehouse: %v", signals)
	}
}

func TestBareTreehouseIsASubshellHolder(t *testing.T) {
	// Bare treehouse is documented as an alias for treehouse get.
	var signals []string
	sys := fake(t, map[string]reply{
		all:                          {out: "  500     1 /usr/local/bin/treehouse\n  501   500 /bin/zsh -i -l\n  600     1 treehouse --version\n"},
		"lsof -a -d cwd -Fpn -p 501": {out: "p501\nfcwd\nn/pool/5/repo\n"},
	}, &signals)
	want := []terminalpkg.Pane{{ID: "500", PID: 500, Label: "/usr/local/bin/treehouse", Command: "/bin/zsh -i -l", Dir: "/pool/5/repo", ChildPIDs: []int{501}}}
	if panes, err := list(context.Background(), sys, local); err != nil || !reflect.DeepEqual(panes, want) {
		t.Fatalf("bare treehouse must be listed: %+v %v", panes, err)
	}
	if p, found, err := lookup(context.Background(), sys, local, "500"); err != nil || !found || !reflect.DeepEqual(p.ChildPIDs, []int{501}) {
		t.Fatalf("bare treehouse lookup: %+v %v %v", p, found, err)
	}
	if _, found, err := lookup(context.Background(), sys, local, "600"); err != nil || found {
		t.Fatalf("a treehouse command with another argument is not a holder: %v %v", found, err)
	}
}

func TestDestroyTracksEveryChildWhenShellOutlivesHolder(t *testing.T) {
	holderAlive, helperAlive := true, true
	var signals []string
	sys := terminalpkg.System{
		Run: func(_ context.Context, _ []string, name string, _ ...string) ([]byte, string, int, error) {
			if name != "ps" {
				t.Fatalf("unexpected command %s", name)
			}
			if holderAlive {
				return []byte("200 100 treehouse get\n203 200 helper --work\n201 200 /bin/zsh -i -l\n"), "", 0, nil
			}
			return []byte("201 1 /bin/zsh -i -l\n"), "", 0, nil
		},
		Process: func(pid int) (string, ownerpkg.Liveness) {
			switch pid {
			case 200:
				if holderAlive {
					return "holder-start", ownerpkg.Alive
				}
			case 203:
				if helperAlive {
					return "helper-start", ownerpkg.Alive
				}
			case 201:
				return "shell-start", ownerpkg.Alive
			}
			return "", ownerpkg.Dead
		},
		Signal: func(pid int, sig syscall.Signal) error {
			signals = append(signals, sig.String()+":"+strconv.Itoa(pid))
			if pid == 203 && sig == syscall.SIGHUP {
				helperAlive = false
			}
			if pid == 200 && sig == syscall.SIGTERM {
				holderAlive = false
			}
			return nil
		},
		Sleep: func(time.Duration) {}, Grace: 0,
	}
	d := New(sys)
	ref, err := d.Prepare(context.Background(), adapterpkg.PrepareRequest{Existing: adapterpkg.ResourceRef{Adopted: true, Adapter: "treehouse-subshell", Name: "200", Terminal: &adapterpkg.TerminalPin{Server: local}}})
	if err != nil || !reflect.DeepEqual(ref.Terminal.Children, []adapterpkg.TerminalChild{{PID: 203, Start: "helper-start"}, {PID: 201, Start: "shell-start"}}) {
		t.Fatalf("adoption did not pin every direct child: %+v %v", ref.Terminal, err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := d.Adapter.Destroy(context.Background(), adapterpkg.Target{Ref: ref}); err == nil || !strings.Contains(err.Error(), "1 held child process(es) survived cleanup") {
			t.Fatalf("Destroy attempt %d error = %v, want surviving shell to retain lease", attempt, err)
		}
	}
	if !reflect.DeepEqual(signals, []string{"hangup:203", "hangup:201", "terminated:200"}) {
		t.Fatalf("cleanup did not signal every pinned child: %v", signals)
	}
}

func TestProcessErrorsStayUnknown(t *testing.T) {
	var signals []string
	for name, r := range map[string]reply{
		"ps failed":       {stderr: "ps: permission denied", code: 1},
		"ps did not run":  {code: -1, err: errors.New("ps did not complete")},
		"malformed row":   {out: "abc def ghi\n"},
		"too few columns": {out: "200\n"},
	} {
		if _, _, err := lookup(context.Background(), fake(t, map[string]reply{all: r}, &signals), local, "200"); err == nil {
			t.Fatalf("%s: treated as proof", name)
		}
		if _, err := list(context.Background(), fake(t, map[string]reply{all: r}, &signals), local); err == nil {
			t.Fatalf("%s: inventory accepted", name)
		}
	}
	if _, err := resolveServer(context.Background(), terminalpkg.System{}, "remote"); err == nil {
		t.Fatal("non-local treehouse server accepted")
	}
	if got, _ := resolveServer(context.Background(), terminalpkg.System{}, ""); got != local {
		t.Fatal("treehouse server must be local")
	}
	if processID.MatchString("0") || processID.MatchString("-1") || !processID.MatchString("48896") {
		t.Fatal("process id syntax")
	}
	ref := adapterpkg.ResourceRef{Name: "200", Terminal: &adapterpkg.TerminalPin{Server: local, PID: 200, Start: "Mon Nov 16 22:32:36 2026"}}
	if got := New(terminalpkg.System{}).Terminal.Describe(ref); got != "treehouse-subshell 200 on local (pid 200 started Mon Nov 16 22:32:36 2026)" {
		t.Fatalf("sweep target must name the subshell once: %q", got)
	}
}
