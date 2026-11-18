package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func scopeCLI(t *testing.T, args ...string) (map[string]any, int) {
	t.Helper()
	var out bytes.Buffer
	code := run(context.Background(), append([]string{"--json"}, args...), &out)
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("%v: %v: %s", args, err, out.String())
	}
	return result, code
}

func TestScopesOptIn(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DIBS_CONFIG", config)
	t.Setenv("DIBS_STATE", t.TempDir())
	t.Setenv("DIBS_OWNER", "pid:"+strconv.Itoa(os.Getpid()))
	for _, args := range [][]string{{"scope", "start"}, {"create", "--owner", "scope:" + strings.Repeat("a", 32)}} {
		result, code := scopeCLI(t, args...)
		if code != 2 || !strings.Contains(result["error"].(string), "DIBS_SCOPES=1") {
			t.Fatalf("%v with scopes off = %d %v", args, code, result)
		}
	}
	if err := os.WriteFile(config, []byte(`{"scopes": true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if result, code := scopeCLI(t, "scope", "start"); code != 0 || result["status"] != "created" {
		t.Fatalf("scope start with scopes on in the config = %d %v", code, result)
	}
}

func TestScopeCommands(t *testing.T) {
	t.Setenv("DIBS_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv("DIBS_STATE", t.TempDir())
	t.Setenv("DIBS_SCOPES", "1")
	owner := "pid:" + strconv.Itoa(os.Getpid())
	started, code := scopeCLI(t, "scope", "start", "--key", "task-7", "--owner", owner)
	if code != 0 || started["status"] != "created" || started["contract"] != float64(1) {
		t.Fatalf("start = %d %v", code, started)
	}
	id := started["id"].(string)
	if again, code := scopeCLI(t, "scope", "start", "--key", "task-7", "--owner", owner); code != 0 || again["status"] != "already_done" || again["id"] != id {
		t.Fatalf("keyed retry = %d %v", code, again)
	}
	for _, args := range [][]string{
		{"scope", "start", "--owner", "scope:" + id},
		{"scope", "start", "--key", "-bad"},
		{"scope", "end"},
		{"scope", "show", id, "--key", "task-7"},
		{"scope", "stop", id},
	} {
		if result, code := scopeCLI(t, args...); code != 2 {
			t.Fatalf("%v = %d %v, want usage error", args, code, result)
		}
	}
	member, code := scopeCLI(t, "create", "--owner", "scope:"+id, "--resources", "lab")
	if code != 0 {
		t.Fatalf("create member = %d %v", code, member)
	}
	shown, code := scopeCLI(t, "scope", "show", "--key", "task-7")
	if code != 0 || shown["status"] != "shown" || shown["count"] != float64(1) || shown["scope"].(map[string]any)["is_scope"] != true {
		t.Fatalf("show = %d %v", code, shown)
	}
	if missing, code := scopeCLI(t, "scope", "show", "--key", "nothing"); code != 3 || missing["reason"] != "not_found" {
		t.Fatalf("show of a missing key = %d %v", code, missing)
	}
	if refused, code := scopeCLI(t, "scope", "end", member["lease"].(map[string]any)["id"].(string)); code != 3 || refused["reason"] != "not_scope" {
		t.Fatalf("end of a member = %d %v", code, refused)
	}
	if preview, code := scopeCLI(t, "scope", "end", id); code != 0 || preview["status"] != "preview" || len(preview["release"].([]any)) != 1 {
		t.Fatalf("end preview = %d %v", code, preview)
	}
	ended, code := scopeCLI(t, "scope", "end", id, "--confirm")
	if code != 0 || ended["status"] != "ended" || ended["forgotten"] != true || len(ended["released"].([]any)) != 1 {
		t.Fatalf("end = %d %v", code, ended)
	}
	if again, code := scopeCLI(t, "scope", "end", id, "--confirm"); code != 0 || again["status"] != "already_done" {
		t.Fatalf("second end = %d %v", code, again)
	}
	if refused, code := scopeCLI(t, "create", "--owner", "scope:"+id, "--resources", "lab"); code != 3 || refused["reason"] != "scope_ended" {
		t.Fatalf("create against ended scope = %d %v, want refusal scope_ended", code, refused)
	}
}
