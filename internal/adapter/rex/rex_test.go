package rex

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	terminalpkg "github.com/Dematom1/dibs/internal/adapter/terminal"
)

type reply struct {
	out, stderr string
	code        int
	err         error
}

const (
	shell = "block:01a1ccf7-2782-7a35-a6b8-0f18a24dc86a"
	other = "block:01a1ccf7-2782-7a35-a6b8-0f18a24dc86b"
)

func fake(t *testing.T, replies map[string]reply, calls *[]string) terminalpkg.System {
	return terminalpkg.System{Run: func(_ context.Context, env []string, _ string, args ...string) ([]byte, string, int, error) {
		if !reflect.DeepEqual(env, serverWide) {
			t.Fatalf("rex call keeps the caller's session scope: %v", env)
		}
		if len(args) < 3 || args[0] != "--autostart=false" || args[1] != "-S" {
			t.Fatalf("rex call may autostart or retarget a server: %v", args)
		}
		key := strings.Join(args[3:], " ")
		*calls = append(*calls, key)
		r, ok := replies[key]
		if !ok {
			t.Fatalf("unexpected rex call %q", key)
		}
		return []byte(r.out), r.stderr, r.code, r.err
	}}
}

func TestLookupAndListMapping(t *testing.T) {
	var calls []string
	replies := map[string]reply{
		"ls --json":                      {out: `{"sessions":[{"session_id":"session:a"},{"session_id":"session:b"}]}`},
		"block ls -s session:a --json":   {out: `{"blocks":[{"block_id":"` + shell + `","label":"fm-task","flavor":"com.superlogical.terminal.shell"},{"block_id":"block:x","flavor":"com.superlogical.browser"}]}`},
		"block ls -s session:b --json":   {out: `{"blocks":[]}`},
		"block call process -b " + shell: {out: `{"last_exit":null,"child":{"pid":46761,"user":"root","name":"login"},"foreground":{"pid":87484,"name":"2.1.322","argv0":"/home/u/.local/bin/claude","cwd":"/work"}}`},
		"block call process -b " + other: {stderr: `Error: block target "` + other + `" not found`, code: 3},
		"block close " + shell:           {},
	}
	sys := fake(t, replies, &calls)
	panes, err := list(context.Background(), sys, "unix:///rex.sock")
	want := []terminalpkg.Pane{{ID: shell, PID: 46761, Label: "fm-task", Command: "claude", Dir: "/work"}}
	if err != nil || !reflect.DeepEqual(panes, want) {
		t.Fatalf("panes %+v %v", panes, err)
	}
	if _, found, err := lookup(context.Background(), sys, "unix:///rex.sock", other); found || err != nil {
		t.Fatalf("not found on a reachable server must prove absence: %v %v", found, err)
	}
	if err := closeBlock(context.Background(), sys, "unix:///rex.sock", terminalpkg.Pane{ID: shell}); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]reply{
		"server not running": {stderr: "Error: no Rex server is running at /x, and autostart is disabled", code: 4},
		"other failure":      {stderr: "Error: denied", code: 1},
		"did not run":        {code: -1, err: errors.New("rex did not complete")},
		"malformed output":   {out: "not json"},
	} {
		_, _, err := lookup(context.Background(), fake(t, map[string]reply{"block call process -b " + shell: r}, &calls), "unix:///rex.sock", shell)
		if err == nil {
			t.Fatalf("%s: treated as proof", name)
		}
	}
	if p, found, err := lookup(context.Background(), fake(t, map[string]reply{"block call process -b " + shell: {out: `{"child":null}`}}, &calls), "unix:///rex.sock", shell); err != nil || !found || p.PID != 0 {
		t.Fatalf("block without a program: %+v %v %v", p, found, err)
	}
	for name, r := range map[string]reply{
		"no sessions field": {out: `{}`},
		"session list fail": {stderr: "boom", code: 1},
	} {
		if _, err := list(context.Background(), fake(t, map[string]reply{"ls --json": r}, &calls), "unix:///rex.sock"); err == nil {
			t.Fatalf("%s: inventory accepted", name)
		}
	}
	bad := map[string]reply{"ls --json": replies["ls --json"], "block ls -s session:a --json": {out: `{"blocks":[{"block_id":"block:short","flavor":"com.superlogical.terminal.shell"}]}`}}
	if _, err := list(context.Background(), fake(t, bad, &calls), "unix:///rex.sock"); err == nil {
		t.Fatal("malformed block id accepted")
	}
}

func TestServerResolution(t *testing.T) {
	var calls []string
	sys := terminalpkg.System{Run: func(_ context.Context, _ []string, _ string, args ...string) ([]byte, string, int, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[len(args)-2] != "whoami" {
			t.Fatalf("unexpected call %v", args)
		}
		return []byte(`{"endpoint":"unix:///rex.sock","pid":1}`), "", 0, nil
	}}
	if got, err := resolveServer(context.Background(), sys, ""); err != nil || got != "unix:///rex.sock" || calls[0] != "--autostart=false whoami --json" {
		t.Fatalf("default server: %q %v %v", got, err, calls)
	}
	if got, _ := resolveServer(context.Background(), sys, "laptop"); got != "unix:///rex.sock" || calls[1] != "--autostart=false -S laptop whoami --json" {
		t.Fatalf("named server: %q %v", got, calls)
	}
	empty := terminalpkg.System{Run: func(context.Context, []string, string, ...string) ([]byte, string, int, error) {
		return []byte(`{}`), "", 0, nil
	}}
	if _, err := resolveServer(context.Background(), empty, ""); err == nil {
		t.Fatal("server without endpoint accepted")
	}
	if !blockID.MatchString(shell) || blockID.MatchString("block:1") || blockID.MatchString("session:"+shell[6:]) {
		t.Fatal("block id syntax")
	}
}
