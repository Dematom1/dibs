package herdr

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

func fake(t *testing.T, replies map[string]reply, calls *[]string) terminalpkg.System {
	return terminalpkg.System{Run: func(_ context.Context, env []string, name string, args ...string) ([]byte, string, int, error) {
		if !reflect.DeepEqual(env, isolated) || name != "herdr" || len(args) < 2 || args[len(args)-2] != "--session" {
			t.Fatalf("herdr call not pinned to its session: %v %s %v", env, name, args)
		}
		key := strings.Join(args, " ")
		*calls = append(*calls, key)
		r, ok := replies[key]
		if !ok {
			t.Fatalf("unexpected herdr call %q", key)
		}
		return []byte(r.out), r.stderr, r.code, r.err
	}}
}

const listed = `{"id":"cli:pane:list","result":{"type":"pane_list","panes":[{"pane_id":"w1:p1","label":null,"title":"build","cwd":"/work","agent":"claude"},{"pane_id":"w1:p2","cwd":null}]}}`

func process(id, pid string) string {
	return `{"id":"x","result":{"type":"pane_process_info","process_info":{"pane_id":"` + id + `","shell_pid":` + pid + `,"foreground_processes":[]}}}`
}

func TestListMapping(t *testing.T) {
	var calls []string
	replies := map[string]reply{
		"pane list --session lab":                          {out: listed},
		"pane process-info --pane w1:p1 --session lab":     {out: process("w1:p1", "41")},
		"pane process-info --pane w1:p2 --session lab":     {out: process("w1:p2", "null")},
		"pane close w1:p1 --session lab":                   {out: `{"id":"x","result":{"type":"ok"}}`},
		"pane process-info --pane w1:p1 --session mixedup": {out: process("w9:p9", "41")},
	}
	panes, err := list(context.Background(), fake(t, replies, &calls), "lab")
	want := []terminalpkg.Pane{{ID: "w1:p1", PID: 41, Label: "build", Command: "claude", Dir: "/work"}, {ID: "w1:p2"}}
	if err != nil || !reflect.DeepEqual(panes, want) {
		t.Fatalf("panes %+v %v", panes, err)
	}
	if err := closePane(context.Background(), fake(t, replies, &calls), "lab", terminalpkg.Pane{ID: "w1:p1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := shellPID(context.Background(), fake(t, replies, &calls), "mixedup", "w1:p1"); err == nil {
		t.Fatal("process info for another pane accepted")
	}
	for name, tc := range map[string]struct {
		r      reply
		absent bool
	}{
		"server not running":   {r: reply{stderr: `{"id":"cli:pane:list","error":{"code":"server_not_running","message":"no herdr server"}}`, code: 1}, absent: true},
		"other API error":      {r: reply{out: `{"id":"x","error":{"code":"internal","message":"boom"}}`, code: 1}},
		"non-JSON failure":     {r: reply{stderr: "panic", code: 101}},
		"missing panes field":  {r: reply{out: `{"id":"x","result":{"type":"pane_list"}}`}},
		"malformed pane id":    {r: reply{out: `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"-x"}]}}`}},
		"exit without result":  {r: reply{code: 1}},
		"command did not run":  {r: reply{code: -1, err: errors.New("herdr did not complete: not found")}},
		"result is not object": {r: reply{out: `{"id":"x","result":{"panes":"nope"}}`}},
	} {
		_, err := list(context.Background(), fake(t, map[string]reply{"pane list --session lab": tc.r}, &calls), "lab")
		if err == nil || tc.absent != errors.Is(err, terminalpkg.ErrNoServer) {
			t.Fatalf("%s: error %v, absence proof %v", name, err, tc.absent)
		}
	}
}

func TestServerResolution(t *testing.T) {
	t.Setenv("HERDR_SESSION", "")
	if got, _ := resolveServer(context.Background(), terminalpkg.System{}, ""); got != "default" {
		t.Fatalf("default session: %q", got)
	}
	t.Setenv("HERDR_SESSION", "lab-x")
	if got, _ := resolveServer(context.Background(), terminalpkg.System{}, ""); got != "lab-x" {
		t.Fatalf("inherited session: %q", got)
	}
	if got, _ := resolveServer(context.Background(), terminalpkg.System{}, "work"); got != "work" {
		t.Fatalf("explicit session: %q", got)
	}
	if _, err := resolveServer(context.Background(), terminalpkg.System{}, "../etc"); err == nil {
		t.Fatal("path-like session accepted")
	}
}
