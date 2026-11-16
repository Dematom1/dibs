package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	terminalpkg "github.com/Dematom1/dibs/internal/adapter/terminal"
)

func fake(out, stderr string, code int, err error, calls *[][]string) terminalpkg.System {
	return terminalpkg.System{Run: func(_ context.Context, _ []string, name string, args ...string) ([]byte, string, int, error) {
		*calls = append(*calls, append([]string{name}, args...))
		return []byte(out), stderr, code, err
	}}
}

func TestListMapping(t *testing.T) {
	var calls [][]string
	panes, err := list(context.Background(), fake("%1\t42\tzsh\t/work\tdev:0.1\n%7\t43\tclaude\t/x\tdev:1.0\n", "", 0, nil, &calls), "/sock")
	want := []terminalpkg.Pane{{ID: "%1", PID: 42, Command: "zsh", Dir: "/work", Label: "dev:0.1"}, {ID: "%7", PID: 43, Command: "claude", Dir: "/x", Label: "dev:1.0"}}
	if err != nil || !reflect.DeepEqual(panes, want) {
		t.Fatalf("panes %+v %v", panes, err)
	}
	if !reflect.DeepEqual(calls[0], []string{"tmux", "-S", "/sock", "list-panes", "-a", "-F", format}) {
		t.Fatalf("list must pin the socket: %v", calls[0])
	}
	if panes, err := list(context.Background(), fake("", "", 0, nil, &calls), "/sock"); err != nil || len(panes) != 0 {
		t.Fatalf("empty server: %v %v", panes, err)
	}
	for name, tc := range map[string]struct {
		out, stderr string
		code        int
		err         error
		absent      bool
	}{
		"no server running": {stderr: "no server running on /sock\n", code: 1, absent: true},
		"socket missing":    {stderr: "error connecting to /sock (No such file or directory)\n", code: 1, absent: true},
		"permission denied": {stderr: "error connecting to /sock (Permission denied)\n", code: 1},
		"other exit":        {stderr: "unknown command\n", code: 1},
		"missing binary":    {err: errors.New("tmux did not complete: executable file not found"), code: -1},
		"too few fields":    {out: "%1\t42\tzsh\n"},
		"bad pane id":       {out: "1\t42\tzsh\t/w\tdev:0.0\n"},
		"bad pid":           {out: "%1\tx\tzsh\t/w\tdev:0.0\n"},
	} {
		_, err := list(context.Background(), fake(tc.out, tc.stderr, tc.code, tc.err, &calls), "/sock")
		if tc.absent != errors.Is(err, terminalpkg.ErrNoServer) || err == nil {
			t.Fatalf("%s: error %v, absence proof %v", name, err, tc.absent)
		}
	}
}

func TestCloseAndServer(t *testing.T) {
	var calls [][]string
	if err := closePane(context.Background(), fake("", "", 0, nil, &calls), "/sock", terminalpkg.Pane{ID: "%3"}); err != nil || !reflect.DeepEqual(calls[0], []string{"tmux", "-S", "/sock", "kill-pane", "-t", "%3"}) {
		t.Fatalf("close: %v %v", calls, err)
	}
	if err := closePane(context.Background(), fake("", "can't find pane", 1, nil, &calls), "/sock", terminalpkg.Pane{ID: "%3"}); err == nil {
		t.Fatal("failed kill-pane accepted")
	}
	dir := t.TempDir()
	t.Setenv("TMUX", dir+"/s,123,0")
	if got, err := resolveServer(context.Background(), terminalpkg.System{}, ""); err != nil || got != mustReal(t, dir)+"/s" {
		t.Fatalf("server from $TMUX: %q %v", got, err)
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", dir)
	if got, _ := resolveServer(context.Background(), terminalpkg.System{}, ""); got != mustReal(t, dir)+"/tmux-"+strconv.Itoa(os.Getuid())+"/default" {
		t.Fatalf("default socket: %q", got)
	}
	if _, err := resolveServer(context.Background(), terminalpkg.System{}, "relative/sock"); err == nil {
		t.Fatal("relative socket accepted")
	}
	if paneID.MatchString("%") || paneID.MatchString("12") || !paneID.MatchString("%12") {
		t.Fatal("pane id syntax")
	}
}

func mustReal(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}
