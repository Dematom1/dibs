package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	policypkg "github.com/Dematom1/dibs/internal/policy"
)

func TestHandoffReselectsTheNewOwnersRow(t *testing.T) {
	t.Setenv("DIBS_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	app := testApplication()
	t.Setenv("DIBS_LEASE_POLICIES", "0")
	if app.handoffPolicy() != nil {
		t.Fatal("policies off re-selected a row at handoff")
	}
	t.Setenv("DIBS_LEASE_POLICIES", "1")
	resolve := app.handoffPolicy()
	for owner, want := range map[string]policypkg.Policy{
		"scope": {Row: "treehouse-worktree/scope", TTL: 30 * time.Minute, Renew: 30 * time.Minute, Grace: 10 * time.Minute},
		"pr":    {Row: "treehouse-worktree/pr", TTL: 8 * time.Hour, Renew: 8 * time.Hour, Grace: 10 * time.Minute},
		"pid":   {Row: "treehouse-worktree", TTL: 8 * time.Hour},
	} {
		p, err := resolve("treehouse-worktree", owner)
		if err != nil || p.Row != want.Row || p.Binding != policypkg.Owner || p.TTL != want.TTL || p.Renew != want.Renew || p.Grace != want.Grace {
			t.Fatalf("worktree handed to %s = %+v, %v; want %+v", owner, p, err, want)
		}
	}
}

func TestHandoffCommand(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DIBS_CONFIG", config)
	t.Setenv("DIBS_STATE", t.TempDir())
	t.Setenv("DIBS_SCOPES", "1")
	t.Setenv("DIBS_CONSUMER_CONTRACT", "1")
	task, pr := "pid:"+strconv.Itoa(os.Getpid()), "pid:"+strconv.Itoa(os.Getppid())
	started, code := scopeCLI(t, "scope", "start", "--owner", task)
	if code != 0 {
		t.Fatalf("scope start = %d %v", code, started)
	}
	scope := started["id"].(string)
	member, code := scopeCLI(t, "create", "--owner", "scope:"+scope, "--resources", "worktree")
	if code != 0 {
		t.Fatalf("create member = %d %v", code, member)
	}
	id := member["lease"].(map[string]any)["id"].(string)
	if result, code := scopeCLI(t, "handoff", id, "--owner", pr); code != 2 || !strings.Contains(result["error"].(string), "DIBS_HANDOFF=1") {
		t.Fatalf("handoff while off = %d %v", code, result)
	}
	if err := os.WriteFile(config, []byte(`{"handoff": true}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"handoff", id}, {"handoff", "--owner", pr}, {"handoff", "nope", "--owner", pr}, {"handoff", id, "--owner", "bogus"}} {
		if result, code := scopeCLI(t, args...); code != 2 {
			t.Fatalf("%v = %d %v, want usage error", args, code, result)
		}
	}
	preview, code := scopeCLI(t, "handoff", id, "--owner", pr)
	if code != 0 || preview["status"] != "preview" || preview["contract"] != float64(1) || preview["from"] != "scope:"+scope || preview["left_scope"] != scope || preview["liveness"] != "alive" {
		t.Fatalf("preview = %d %v", code, preview)
	}
	if done, code := scopeCLI(t, "handoff", id, "--owner", pr, "--confirm"); code != 0 || done["status"] != "handed_off" {
		t.Fatalf("handoff = %d %v", code, done)
	}
	if again, code := scopeCLI(t, "handoff", id, "--owner", pr, "--confirm"); code != 0 || again["status"] != "already_done" {
		t.Fatalf("retry = %d %v", code, again)
	}
	explained, code := scopeCLI(t, "explain", id)
	if handoff, _ := explained["handoff"].(map[string]any); code != 0 || handoff["from"] != "scope:"+scope || handoff["at"] == "" {
		t.Fatalf("explain = %d %v", code, explained)
	}
	if ended, code := scopeCLI(t, "scope", "end", scope, "--confirm"); code != 0 || len(ended["released"].([]any)) != 0 || ended["forgotten"] != true {
		t.Fatalf("scope end = %d %v", code, ended)
	}
	if refused, code := scopeCLI(t, "handoff", id, "--owner", "scope:"+scope); code != 3 || refused["reason"] != "owner_not_alive" {
		t.Fatalf("handoff to an ended scope = %d %v", code, refused)
	}
	if refused, code := scopeCLI(t, "handoff", strings.Repeat("f", 32), "--owner", pr); code != 3 || refused["reason"] != "not_found" {
		t.Fatalf("handoff of a missing lease = %d %v", code, refused)
	}
}
