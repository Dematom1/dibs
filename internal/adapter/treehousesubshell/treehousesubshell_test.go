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
)

type reply struct {
	out, stderr string
	code        int
	err         error
}

const table = `  100     1 /usr/bin/login -flp user /bin/bash
  200   100 /nix/store/abc-treehouse/bin/treehouse get
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
		all:                                 {out: table},
		"lsof -a -d cwd -Fpn -p 201,301":    {out: "p201\nfcwd\nn/pool/1/repo\np301\nfcwd\nn/pool/3/repo\n"},
		"-p 200 -ww -o pid=,ppid=,command=": {out: "  200   100 treehouse get\n"},
		"-p 202 -ww -o pid=,ppid=,command=": {out: "  202   201 claude\n"},
		"-p 999 -ww -o pid=,ppid=,command=": {code: 1},
	}, &signals)
	panes, err := list(context.Background(), sys, local)
	want := []terminalpkg.Pane{{ID: "200", PID: 200, Label: "/nix/store/abc-treehouse/bin/treehouse get", Command: "/bin/zsh -i -l", Dir: "/pool/1/repo"}, {ID: "300", PID: 300, Label: "treehouse enter wt-3", Command: "/bin/zsh -i -l", Dir: "/pool/3/repo"}}
	if err != nil || !reflect.DeepEqual(panes, want) {
		t.Fatalf("panes %+v %v", panes, err)
	}
	if p, found, err := lookup(context.Background(), sys, local, "200"); err != nil || !found || p.PID != 200 {
		t.Fatalf("live subshell holder: %+v %v %v", p, found, err)
	}
	if _, found, err := lookup(context.Background(), sys, local, "202"); err != nil || found {
		t.Fatalf("a reused PID that is not treehouse must read as absent: %v %v", found, err)
	}
	if _, found, err := lookup(context.Background(), sys, local, "999"); err != nil || found {
		t.Fatalf("exited process: %v %v", found, err)
	}
	hangup(context.Background(), sys, terminalpkg.Pane{ID: "200", PID: 200})
	if err := closeSubshell(context.Background(), sys, local, terminalpkg.Pane{ID: "200", PID: 200}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(signals, []string{"hangup:201", "terminated:200"}) {
		t.Fatalf("hangup must reach only the held subshell, then treehouse: %v", signals)
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
		if _, _, err := lookup(context.Background(), fake(t, map[string]reply{"-p 200 -ww -o pid=,ppid=,command=": r}, &signals), local, "200"); err == nil {
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
