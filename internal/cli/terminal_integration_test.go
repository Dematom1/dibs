//go:build integration

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type tmuxLab struct {
	t      *testing.T
	socket string
}

func (lab tmuxLab) tmux(args ...string) string {
	lab.t.Helper()
	out, err := exec.Command("tmux", append([]string{"-f", "/dev/null", "-S", lab.socket}, args...)...).CombinedOutput()
	if err != nil {
		lab.t.Fatalf("tmux %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// start launches a fresh server on the lab socket with three interactive /bin/sh panes and returns pane ID to shell PID.
func (lab tmuxLab) start() map[string]int {
	lab.t.Helper()
	lab.tmux("new-session", "-d", "-s", "dibs", "-x", "120", "-y", "40", "/bin/sh", "-i")
	lab.tmux("split-window", "-t", "dibs", "/bin/sh", "-i")
	lab.tmux("split-window", "-t", "dibs", "/bin/sh", "-i")
	return lab.panes()
}

func (lab tmuxLab) panes() map[string]int {
	lab.t.Helper()
	out, err := exec.Command("tmux", "-f", "/dev/null", "-S", lab.socket, "list-panes", "-a", "-F", "#{pane_id} #{pane_pid}").Output()
	panes := map[string]int{}
	if err != nil {
		return panes
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if id, pid, ok := strings.Cut(line, " "); ok {
			panes[id], _ = strconv.Atoi(pid)
		}
	}
	return panes
}

func ownerProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func dibsJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	return dibsCode(t, 0, args...)
}

// dibsCode runs dibs as a non-interactive caller would and requires an exact exit code and JSON output.
func dibsCode(t *testing.T, want int, args ...string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if code := run(context.Background(), append([]string{"--json"}, args...), &out); code != want {
		t.Fatalf("dibs %v exited %d, want %d: %s", args, code, want, out.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("dibs %v output: %v %s", args, err, out.String())
	}
	return result
}

// dibsAXI runs dibs the way an agent does and requires an exact exit code and every TOON fragment.
func dibsAXI(t *testing.T, want int, fragments []string, args ...string) {
	t.Helper()
	var out bytes.Buffer
	if code := run(context.Background(), append([]string{"--axi"}, args...), &out); code != want {
		t.Fatalf("dibs --axi %v exited %d, want %d: %s", args, code, want, out.String())
	}
	for _, fragment := range fragments {
		if !strings.Contains(out.String(), fragment) {
			t.Fatalf("dibs --axi %v lacks %q:\n%s", args, fragment, out.String())
		}
	}
}

func strs(v any) []string {
	items, _ := v.([]any)
	out := []string{}
	for _, item := range items {
		out = append(out, item.(string))
	}
	sort.Strings(out)
	return out
}

func TestIntegrationTmuxTerminalLeases(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatalf("tmux executable required for terminal integration: %v", err)
	}
	dir, err := os.MkdirTemp("", "dibs-tmux-*")
	if err != nil {
		t.Fatal(err)
	}
	lab := tmuxLab{t, filepath.Join(dir, "s")}
	t.Cleanup(func() {
		// Kill only this lab's server, by its explicit socket.
		if _, err := os.Stat(lab.socket); err == nil {
			exec.Command("tmux", "-S", lab.socket, "kill-server").Run()
		}
		os.RemoveAll(dir)
	})
	t.Setenv("DIBS_STATE", filepath.Join(dir, "state"))
	t.Setenv("DIBS_TERMINAL_ADAPTERS", "tmux")
	// The repeated-release assertions below are the pre-contract output; an exported contract opt-in must not change them.
	t.Setenv("DIBS_CONSUMER_CONTRACT", "0")
	first := lab.start()
	if len(first) != 3 {
		t.Fatalf("lab panes: %v", first)
	}
	orphans := dibsJSON(t, "orphans", "--type", "tmux", "--server", lab.socket)
	if orphans["count"] != float64(3) {
		t.Fatalf("all unleased panes must be listed: %v", orphans)
	}
	server := orphans["orphans"].([]any)[0].(map[string]any)["server"].(string)
	dibsAXI(t, 0, []string{"orphans[3]{adopt,command,dir,label,name,pid,server,start,type}:", "total: 3", "unavailable:\n"}, "orphans", "--type", "tmux", "--server", lab.socket)
	owners := map[string]*exec.Cmd{"%0": ownerProcess(t), "%1": ownerProcess(t), "%2": ownerProcess(t)}
	leases := map[string]string{}
	for pane, owner := range owners {
		adopted := dibsJSON(t, "adopt", "tmux", pane, "--server", lab.socket, "--owner", "pid:"+strconv.Itoa(owner.Process.Pid), "--ttl", "1h")
		leases[pane] = adopted["lease"].(map[string]any)["id"].(string)
	}
	retry := dibsJSON(t, "adopt", "tmux", "%1", "--server", lab.socket, "--owner", "pid:"+strconv.Itoa(owners["%1"].Process.Pid), "--ttl", "1h")
	if retry["existing"] != true || retry["lease"].(map[string]any)["id"] != leases["%1"] {
		t.Fatalf("a same-owner adopt retry must return the existing lease: %v", retry)
	}
	pin := retry["lease"].(map[string]any)["terminal"].(map[string]any)
	if pin["server"] != server || pin["pid"] != float64(first["%1"]) || pin["start"] == "" {
		t.Fatalf("adopt must report the pinned shell: %v", pin)
	}
	dibsAXI(t, 0, []string{"existing: true", "  terminal:\n    pid: " + strconv.Itoa(first["%1"]) + "\n    server: ", "help[2]: This owner already holds the lease; run dibs extend " + leases["%1"]}, "adopt", "tmux", "%1", "--server", lab.socket, "--owner", "pid:"+strconv.Itoa(owners["%1"].Process.Pid), "--ttl", "1h")
	dibsAXI(t, 1, []string{"code: COMMAND_FAILED", "exit_code: 1"}, "adopt", "tmux", "%1", "--server", lab.socket, "--owner", "pid:"+strconv.Itoa(owners["%2"].Process.Pid), "--ttl", "1h")
	if refused := dibsCode(t, 1, "adopt", "tmux", "%1", "--server", lab.socket, "--owner", "pid:"+strconv.Itoa(owners["%2"].Process.Pid), "--ttl", "1h"); refused["code"] != "COMMAND_FAILED" {
		t.Fatalf("another owner must not take a leased pane: %v", refused)
	}
	if invalid := dibsCode(t, 2, "adopt", "tmux", "1", "--server", lab.socket, "--owner", "pid:"+strconv.Itoa(owners["%1"].Process.Pid), "--ttl", "1h"); invalid["code"] != "VALIDATION_ERROR" {
		t.Fatalf("an invalid pane ID must be a usage error: %v", invalid)
	}
	if orphans := dibsJSON(t, "orphans", "--type", "tmux", "--server", lab.socket); orphans["count"] != float64(0) {
		t.Fatalf("leased panes listed as orphans: %v", orphans)
	}
	if sweep := dibsJSON(t, "sweep"); len(strs(sweep["eligible"])) != 0 {
		t.Fatalf("live owners' panes offered for reclaim: %v", sweep)
	}

	owners["%0"].Process.Kill()
	owners["%0"].Wait()
	preview := dibsJSON(t, "sweep")
	targets, _ := preview["targets"].(map[string]any)
	wantTarget := "tmux pane %0 on " + server + " (pid " + strconv.Itoa(first["%0"]) + " started "
	if !reflect.DeepEqual(strs(preview["eligible"]), []string{leases["%0"]}) || len(targets) != 1 || !strings.HasPrefix(targets[leases["%0"]].(string), wantTarget) {
		t.Fatalf("dry run must list exactly the dead owner's pane: %v", preview)
	}
	dibsAXI(t, 0, []string{"eligible[1]: " + leases["%0"], "targets:\n  ", leases["%0"], `: "` + wantTarget, "help[1]: Run dibs sweep --confirm"}, "sweep")
	if got := lab.panes(); !reflect.DeepEqual(got, first) {
		t.Fatalf("dry run changed panes: %v", got)
	}
	confirmed := dibsJSON(t, "sweep", "--confirm")
	if !reflect.DeepEqual(strs(confirmed["reclaimed"]), []string{leases["%0"]}) {
		t.Fatalf("confirm must reclaim only the dead owner's pane: %v", confirmed)
	}
	after := lab.panes()
	if _, survived := after["%0"]; survived || after["%1"] != first["%1"] || after["%2"] != first["%2"] {
		t.Fatalf("reclaim must close %%0 and leave live owners' panes: %v", after)
	}

	// A restarted server reuses pane IDs for new shells; dibs must forget the stale lease without touching them.
	lab.tmux("kill-server")
	// tmux 3.4 can leave the socket behind after the server exits.
	if err := os.Remove(lab.socket); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	owners["%2"].Process.Kill()
	owners["%2"].Wait()
	second := lab.start()
	if second["%2"] == 0 || second["%2"] == first["%2"] {
		t.Fatalf("restart did not reuse pane IDs with new shells: %v %v", first, second)
	}
	confirmed = dibsJSON(t, "sweep", "--confirm")
	if !reflect.DeepEqual(strs(confirmed["reclaimed"]), []string{leases["%2"]}) || !reflect.DeepEqual(lab.panes(), second) {
		t.Fatalf("a reused pane ID must not be reclaimed by guess: %v %v", confirmed, lab.panes())
	}
	if released := dibsJSON(t, "release", leases["%1"], "--confirm"); released["status"] != "released" || !reflect.DeepEqual(lab.panes(), second) {
		t.Fatalf("release of a lease whose shell is gone must forget it and touch nothing: %v %v", released, lab.panes())
	}
	if again := dibsJSON(t, "release", leases["%1"], "--confirm"); again["status"] != "already absent (no-op)" {
		t.Fatalf("a repeated release must be a no-op: %v", again)
	}
	if list := dibsJSON(t, "ps"); list["count"] != float64(0) {
		t.Fatalf("leases retained: %v", list)
	}
}
