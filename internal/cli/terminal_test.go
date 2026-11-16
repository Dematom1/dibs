package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

func unsetTerminalOptIn(t *testing.T) {
	t.Helper()
	t.Setenv("DIBS_TERMINAL_ADAPTERS", "")
	os.Unsetenv("DIBS_TERMINAL_ADAPTERS")
	t.Setenv("DIBS_CONFIG", "")
	os.Unsetenv("DIBS_CONFIG")
	t.Setenv("BERTH_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestTerminalOptInGatesAdoptAndOrphans(t *testing.T) {
	self := "pid:" + strconv.Itoa(os.Getpid())
	unsetTerminalOptIn(t)
	for _, args := range [][]string{
		{"adopt", "tmux", "%1", "--owner", self, "--ttl", "1h"},
		{"orphans", "--type", "rex"},
	} {
		dir := filepath.Join(t.TempDir(), "untouched")
		t.Setenv("DIBS_STATE", dir)
		var out bytes.Buffer
		if code := run(context.Background(), append([]string{"--json"}, args...), &out); code != 2 || !strings.Contains(out.String(), "terminal_adapters") {
			t.Fatalf("%v with the opt-in absent: code %d %s", args, code, out.String())
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("refused terminal command touched state")
		}
	}
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{"terminal_adapters":["tmux","rex","tmux"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_CONFIG", config)
	if got, err := enabledTerminals(newAdapters()); err != nil || !reflect.DeepEqual(got, []string{"tmux", "rex"}) {
		t.Fatalf("config opt-in: %v %v", got, err)
	}
	t.Setenv("DIBS_TERMINAL_ADAPTERS", "")
	if got, err := enabledTerminals(newAdapters()); err != nil || len(got) != 0 {
		t.Fatalf("an empty environment value must disable a config opt-in: %v %v", got, err)
	}
	t.Setenv("DIBS_TERMINAL_ADAPTERS", "tmux,kind")
	if _, err := enabledTerminals(newAdapters()); err == nil {
		t.Fatal("non-terminal adapter accepted as a terminal opt-in")
	}
}

func TestOrphansRejectsUnknownTerminalOptIn(t *testing.T) {
	unsetTerminalOptIn(t)
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{"terminal_adapters":["tmuxx"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_CONFIG", config)
	state := filepath.Join(t.TempDir(), "untouched")
	t.Setenv("DIBS_STATE", state)
	var out bytes.Buffer
	if code := run(context.Background(), []string{"--json", "orphans"}, &out); code != 2 || !strings.Contains(out.String(), `tmuxx`) || !strings.Contains(out.String(), "terminal_adapters") {
		t.Fatalf("unknown terminal opt-in: code %d, %s", code, out.String())
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid terminal opt-in touched lease state")
	}
}

func TestTerminalUsageBeforeSideEffects(t *testing.T) {
	self := "pid:" + strconv.Itoa(os.Getpid())
	t.Setenv("DIBS_TERMINAL_ADAPTERS", "tmux,herdr,rex,treehouse-subshell")
	for _, args := range [][]string{
		{"adopt", "tmux", "12", "--owner", self, "--ttl", "1h"},
		{"adopt", "tmux", "%1", "--context", "orbstack", "--owner", self, "--ttl", "1h"},
		{"adopt", "kind", "lab", "--server", "/sock", "--owner", self, "--ttl", "1h"},
		{"adopt", "rex", "block:1", "--owner", self, "--ttl", "1h"},
		{"orphans", "--server", "/sock"},
		{"orphans", "--type", "none"},
		{"orphans", "--type", "kind", "--server", "/sock"},
		{"create", "--adapter", "tmux"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "untouched")
			t.Setenv("DIBS_STATE", dir)
			var out bytes.Buffer
			if code := run(context.Background(), args, &out); code != 2 {
				t.Fatalf("code %d: %s", code, out.String())
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("usage error touched state")
			}
		})
	}
}

func TestMissingTerminalToolIsUnavailableNotFatal(t *testing.T) {
	t.Setenv("DIBS_TERMINAL_ADAPTERS", "tmux")
	t.Setenv("DIBS_STATE", t.TempDir())
	t.Setenv("PATH", "")
	args := []string{"orphans", "--type", "tmux", "--server", "/nonexistent/sock"}
	var out bytes.Buffer
	if code := run(context.Background(), append([]string{"--json"}, args...), &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	var result struct {
		Count       int               `json:"count"`
		Unavailable map[string]string `json:"unavailable"`
		Help        []string          `json:"help"`
	}
	hint := "Start the tmux server, or run dibs orphans --type tmux --server S to inventory another one"
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Count != 0 || result.Unavailable["tmux"] == "" || !slices.Contains(result.Help, hint) {
		t.Fatalf("missing tmux must be reported unavailable: %s", out.String())
	}
	out.Reset()
	if code := run(context.Background(), append([]string{"--axi"}, args...), &out); code != 0 || !strings.Contains(out.String(), "total: 0\nunavailable:\n  tmux: ") || !strings.Contains(out.String(), hint) {
		t.Fatalf("AXI must show the unavailable kind and a next step: %s", out.String())
	}
}

func TestTerminalOutputModes(t *testing.T) {
	row := map[string]any{"type": "tmux", "name": "%3", "server": "/tmp/s", "pid": 41, "start": "Thu Nov 12 02:31:42 2026", "command": "zsh", "label": "x:0.0", "dir": "/work"}
	row["adopt"] = adoptionCommand(row)
	pin := &adapterpkg.TerminalPin{Server: "/tmp/s", PID: 41, Start: "Thu Nov 12 02:31:42 2026"}
	l := leasepkg.Lease{ID: strings.Repeat("a", 32), Owner: ownerpkg.Reference{Kind: "pid", ID: "1"}, ResourceRef: adapterpkg.ResourceRef{Adapter: "tmux", Name: "%3", Terminal: pin}}
	target := "tmux pane %3 on /tmp/s (pid 41 started Thu Nov 12 02:31:42 2026)"
	views := map[string]map[string]any{
		"orphans": {"count": 1, "orphans": []map[string]any{row}, "unavailable": map[string]string{"rex": "rex server unreachable"}, "help": []string{"next"}},
		"empty":   {"count": 0, "orphans": []map[string]any{}, "unavailable": map[string]string{}},
		"sweep":   {"confirmed": false, "eligible": []string{l.ID}, "targets": map[string]string{l.ID: target}, "reclaimed": []string{}, "unknown": []string{}},
		"adopt":   {"lease": outputpkg.LeaseView(l, ownerpkg.Alive, l.Created, ""), "existing": true},
	}
	want := map[outputpkg.Mode]map[string][]string{
		outputpkg.Human: {
			"orphans": {"tmux %3 (/tmp/s)", "pid: 41", "unavailable rex: rex server unreachable", "--server '/tmp/s'"},
			"empty":   {"No unleased resources."},
			"sweep":   {"targets " + l.ID + ": " + target},
			"adopt":   {"terminal pid: 41", "terminal server: /tmp/s", "existing: true"},
		},
		outputpkg.AXI: {
			"orphans": {"orphans[1]{adopt,command,dir,label,name,pid,server,start,type}:", ",41,/tmp/s,", "unavailable:\n  rex: rex server unreachable", "help[1]: next"},
			"empty":   {"orphans: []", "total: 0"},
			"sweep":   {"targets:\n  " + l.ID + ": \"" + target + "\""},
			"adopt":   {"existing: true", "  terminal:\n    pid: 41\n    server: /tmp/s\n    start: \"Thu Nov 12 02:31:42 2026\""},
		},
		outputpkg.JSON: {
			"orphans": {`"pid":41`, `"server":"/tmp/s"`, `"unavailable":{"rex":"rex server unreachable"}`},
			"empty":   {`"unavailable":{}`},
			"sweep":   {`"targets":{"` + l.ID + `":"` + target + `"}`},
			"adopt":   {`"existing":true`, `"terminal":{"pid":41,"server":"/tmp/s","start":"Thu Nov 12 02:31:42 2026"}`},
		},
	}
	for mode, cases := range want {
		for name, fragments := range cases {
			var out bytes.Buffer
			outputpkg.Emit(&out, mode, views[name])
			for _, fragment := range fragments {
				if !strings.Contains(out.String(), fragment) {
					t.Errorf("%s %s lacks %q:\n%s", mode, name, fragment, out.String())
				}
			}
		}
	}
}

func TestTerminalChildIdentityView(t *testing.T) {
	pin := &adapterpkg.TerminalPin{Server: "/sock", PID: 200, Start: "holder-start", ChildPID: 201, ChildStart: "shell-start"}
	l := leasepkg.Lease{ResourceRef: adapterpkg.ResourceRef{Adapter: "treehouse-subshell", Name: "200", Terminal: pin}}
	view := outputpkg.LeaseView(l, ownerpkg.Alive, l.Created, "")
	terminal := view["terminal"].(map[string]any)
	if terminal["child_pid"] != 201 || terminal["child_start"] != "shell-start" {
		t.Fatalf("lease view lost held child identity: %#v", terminal)
	}
	for _, mode := range []outputpkg.Mode{outputpkg.Human, outputpkg.AXI, outputpkg.JSON} {
		var out bytes.Buffer
		outputpkg.Emit(&out, mode, map[string]any{"lease": view})
		if !strings.Contains(out.String(), "201") || !strings.Contains(out.String(), "shell-start") {
			t.Errorf("%s output lost held child identity: %s", mode, out.String())
		}
	}
}

func TestTerminalAdoptionCommand(t *testing.T) {
	got := adoptionCommand(map[string]any{"type": "tmux", "name": "%3", "server": "/tmp/tmux-501/default"})
	if got != "dibs adopt tmux '%3' --server '/tmp/tmux-501/default' --owner '<owner>' --ttl '<duration>'" {
		t.Fatalf("template: %s", got)
	}
}
