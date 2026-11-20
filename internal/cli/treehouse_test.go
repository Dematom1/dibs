package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	composepkg "github.com/Dematom1/dibs/internal/adapter/compose"
	k3dpkg "github.com/Dematom1/dibs/internal/adapter/k3d"
	kindpkg "github.com/Dematom1/dibs/internal/adapter/kind"
	nonepkg "github.com/Dematom1/dibs/internal/adapter/none"
	treehousepkg "github.com/Dematom1/dibs/internal/adapter/treehouseworktree"
	configpkg "github.com/Dematom1/dibs/internal/config"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
)

// fakePool is a one-slot treehouse pool and its git checkout, answering only the calls the adapter makes.
type fakePool struct {
	repo, slot, claim, holder, dirty, statusOverride string
	processes                                        []any
}

func (p *fakePool) run(_ context.Context, _ string, _ []string, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	switch {
	case strings.Contains(call, "rev-parse --path-format=absolute --git-common-dir"):
		if strings.Contains(call, "-C /work/other") {
			return []byte("/work/other/.git\n"), nil
		}
		return []byte(p.repo + "/.git\n"), nil
	case strings.HasSuffix(call, "rev-parse HEAD"):
		return []byte("head-A\n"), nil
	case strings.HasPrefix(call, "treehouse get --lease --json --lease-holder "):
		p.claim, p.holder = "tok-A", args[4]
		return json.Marshal(map[string]string{"path": p.slot, "lease_id": p.claim, "lease_holder": p.holder})
	case strings.HasPrefix(call, "treehouse status --json"):
		if p.statusOverride != "" {
			return []byte(p.statusOverride), nil
		}
		status := "available"
		if p.claim != "" {
			status = "leased"
		}
		return json.Marshal([]map[string]any{{"name": "3", "path": p.slot, "status": status, "lease_id": p.claim, "lease_holder": p.holder, "processes": append([]any{}, p.processes...)}})
	case strings.HasSuffix(call, "status --porcelain --untracked-files=all"):
		return []byte(p.dirty), nil
	case strings.Contains(call, " log --format=%H HEAD --not "):
		return nil, nil
	case strings.HasPrefix(call, "treehouse return "+p.slot+" --if-lease-id "+p.claim):
		p.claim, p.holder = "", ""
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected call %q", call)
}

func fakeTreehouseApp(pool *fakePool) application {
	resources := adapterpkg.NewRegistry(nonepkg.New(), kindpkg.New(adapterpkg.RunCommand), k3dpkg.New(adapterpkg.RunCommand), composepkg.New(adapterpkg.RunCommand, false), treehousepkg.New(pool.run, true))
	owners := newOwners()
	return application{version, resources, owners, leasepkg.New(leasepkg.Options{Adapters: resources, Owners: owners})}
}

func drive(t *testing.T, app application, mode outputpkg.Mode, want int, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if got := app.runWithOutput(context.Background(), args, &out, mode); got != want {
		t.Fatalf("%v exited %d, want %d: %s", args, got, want, out.String())
	}
	return out.String()
}

func TestTreehouseWorktreeOutputModes(t *testing.T) {
	t.Setenv("DIBS_CONSUMER_CONTRACT", "1")
	t.Setenv("DIBS_STATE", t.TempDir())
	t.Setenv("TREEHOUSE_ROOT", "")
	fixedBudget(t)
	pool := &fakePool{repo: "/work/app", slot: "/pool/app-1/3/app"}
	app := fakeTreehouseApp(pool)
	owner := "pid:" + strconv.Itoa(os.Getpid())

	var created map[string]any
	if err := json.Unmarshal([]byte(drive(t, app, outputpkg.JSON, 0, "create", "--adapter", treehousepkg.Name, "--repo", "/work/app/sub", "--owner", owner, "--ttl", "1h", "--landed-exclude-remote", "gate", "--key", "worktree-key")), &created); err != nil {
		t.Fatal(err)
	}
	lease, record := created["lease"].(map[string]any), created["record"].(map[string]any)
	resource := record["resource"].(map[string]any)
	id := lease["id"].(string)
	var retry map[string]any
	if err := json.Unmarshal([]byte(drive(t, app, outputpkg.JSON, 0, "create", "--adapter", treehousepkg.Name, "--repo", "/work/app/sub", "--owner", owner, "--ttl", "1h", "--landed-exclude-remote", "gate", "--key", "worktree-key")), &retry); err != nil || retry["status"] != "already_done" || retry["lease"].(map[string]any)["id"] != id {
		t.Fatalf("same keyed retry = %v, %v", retry, err)
	}
	for name, args := range map[string][]string{
		"different repository": {"create", "--adapter", treehousepkg.Name, "--repo", "/work/other", "--owner", owner, "--ttl", "1h", "--landed-exclude-remote", "gate", "--key", "worktree-key"},
		"different exclusions": {"create", "--adapter", treehousepkg.Name, "--repo", "/work/app/sub", "--owner", owner, "--ttl", "1h", "--landed-exclude-remote", "mirror", "--key", "worktree-key"},
	} {
		t.Run(name, func(t *testing.T) {
			var conflict map[string]any
			if err := json.Unmarshal([]byte(drive(t, app, outputpkg.JSON, 3, args...)), &conflict); err != nil || conflict["reason"] != "key_conflict" {
				t.Fatalf("keyed mismatch = %v, %v", conflict, err)
			}
		})
	}
	if lease["path"] != pool.slot || lease["resource"] != "treehouse-worktree:3" || resource["treehouse_lease_id"] != "tok-A" || resource["repo"] != "/work/app" || resource["treehouse_lease_holder"] != "dibs:"+id {
		t.Fatalf("create = %v", created)
	}
	if excluded := resource["landed_exclude_remotes"].([]any); len(excluded) != 1 || excluded[0] != "gate" {
		t.Fatalf("exclusions not recorded: %v", resource)
	}
	if hints := created["help"].([]any); !strings.HasPrefix(hints[0].(string), "Work in '"+pool.slot+"'") {
		t.Fatalf("help = %v", hints)
	}
	axi := drive(t, app, outputpkg.AXI, 0, "ps", "--full")
	for _, want := range []string{"path: " + pool.slot, "treehouse_lease_id: tok-A", "resource: \"treehouse-worktree:3\""} {
		if !strings.Contains(axi, want) {
			t.Fatalf("AXI ps --full lacks %q:\n%s", want, axi)
		}
	}

	pool.dirty = " M a.txt\n?? b.txt\n"
	var blocked map[string]any
	if err := json.Unmarshal([]byte(drive(t, app, outputpkg.JSON, 3, "release", id, "--confirm")), &blocked); err != nil {
		t.Fatal(err)
	}
	blockers := blocked["blockers"].(map[string]any)
	if blocked["code"] != "COMMAND_FAILED" || blocked["id"] != id || blockers["uncommitted_count"] != float64(2) || blockers["unpushed_count"] != float64(0) || blockers["uncommitted"].([]any)[1] != "b.txt" {
		t.Fatalf("blocked release = %v", blocked)
	}
	if hints := blocked["help"].([]any); !strings.Contains(hints[0].(string), "dibs release "+id+" --confirm") || !strings.Contains(hints[1].(string), "--discard-unlanded") {
		t.Fatalf("help = %v", hints)
	}
	axi = drive(t, app, outputpkg.AXI, 3, "release", id, "--confirm")
	for _, want := range []string{"blockers:", "uncommitted[2]: a.txt,b.txt", "uncommitted_count: 2", "unpushed_count: 0", "code: COMMAND_FAILED", "help[2]:"} {
		if !strings.Contains(axi, want) {
			t.Fatalf("AXI blocked release lacks %q:\n%s", want, axi)
		}
	}
	if !strings.Contains(drive(t, app, outputpkg.Human, 3, "release", id, "--confirm"), "unlanded work blocks cleanup") {
		t.Fatal("human output hides the refusal")
	}
	sweep := drive(t, app, outputpkg.JSON, 0, "sweep", "--confirm")
	if strings.Contains(sweep, id) {
		t.Fatalf("sweep touched a live, unexpired lease: %s", sweep)
	}
	if pool.claim != "tok-A" {
		t.Fatal("refusal returned the slot")
	}

	pool.dirty = ""
	if got := drive(t, app, outputpkg.AXI, 0, "release", id, "--confirm"); !strings.Contains(got, "status: released") {
		t.Fatalf("release = %s", got)
	}
	if pool.claim != "" {
		t.Fatal("slot was not returned")
	}
	if got := drive(t, app, outputpkg.JSON, 0, "release", id, "--confirm"); !strings.Contains(got, "already_done") {
		t.Fatalf("repeat release = %s", got)
	}
}

func TestTreehouseMalformedStatusRetainsLease(t *testing.T) {
	t.Setenv("DIBS_STATE", t.TempDir())
	t.Setenv("TREEHOUSE_ROOT", "")
	pool := &fakePool{repo: "/work/app", slot: "/pool/app-1/3/app"}
	app := fakeTreehouseApp(pool)
	owner := "pid:" + strconv.Itoa(os.Getpid())
	created := drive(t, app, outputpkg.JSON, 0, "create", "--adapter", treehousepkg.Name, "--repo", "/work/app", "--owner", owner, "--ttl", "1h")
	var result map[string]any
	if err := json.Unmarshal([]byte(created), &result); err != nil {
		t.Fatal(err)
	}
	id := result["lease"].(map[string]any)["id"].(string)
	pool.statusOverride = "null"
	drive(t, app, outputpkg.JSON, 1, "release", id, "--confirm")
	if listing := drive(t, app, outputpkg.JSON, 0, "ps", "--full"); !strings.Contains(listing, id) {
		t.Fatalf("malformed allocator status forgot lease %s: %s", id, listing)
	}
}

func TestTreehouseWorktreeAdoptOutput(t *testing.T) {
	t.Setenv("DIBS_STATE", t.TempDir())
	t.Setenv("TREEHOUSE_ROOT", "")
	pool := &fakePool{repo: "/work/app", slot: "/pool/app-1/3/app", claim: "tok-A", holder: "caller"}
	app := fakeTreehouseApp(pool)
	owner := "pid:" + strconv.Itoa(os.Getpid())
	drive(t, app, outputpkg.JSON, 1, "adopt", treehousepkg.Name, pool.slot, "--lease-id", "tok-wrong", "--owner", owner, "--ttl", "1h")
	axi := drive(t, app, outputpkg.AXI, 0, "adopt", treehousepkg.Name, pool.slot, "--lease-id", "tok-A", "--owner", owner, "--ttl", "1h")
	for _, want := range []string{"path: " + pool.slot, "resource: \"treehouse-worktree:3\"", "Work in '" + pool.slot + "'"} {
		if !strings.Contains(axi, want) {
			t.Fatalf("AXI adopt lacks %q:\n%s", want, axi)
		}
	}
	var duplicate map[string]any
	if err := json.Unmarshal([]byte(drive(t, app, outputpkg.JSON, 1, "adopt", treehousepkg.Name, pool.slot, "--lease-id", "tok-A", "--owner", owner, "--ttl", "1h")), &duplicate); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(duplicate["error"].(string), "already under a lease") {
		t.Fatalf("duplicate adoption = %v", duplicate)
	}
}

func TestTreehouseWorktreeUsageErrors(t *testing.T) {
	t.Setenv("DIBS_STATE", t.TempDir())
	app := fakeTreehouseApp(&fakePool{})
	owner := "pid:" + strconv.Itoa(os.Getpid())
	for name, args := range map[string][]string{
		"repo on another adapter":      {"create", "--repo", "/work/app", "--owner", owner},
		"exclusion on another adapter": {"create", "--adapter", "none", "--landed-exclude-remote", "gate", "--owner", owner},
		"option-like exclusion":        {"create", "--adapter", treehousepkg.Name, "--landed-exclude-remote", "--all", "--owner", owner},
		"discard without confirm":      {"release", strings.Repeat("a", 32), "--discard-unlanded"},
		"relative adopt path":          {"adopt", treehousepkg.Name, "pool/3/app", "--lease-id", "tok-A", "--owner", owner, "--ttl", "1h"},
		"adopt without lease id":       {"adopt", treehousepkg.Name, "/pool/3/app", "--owner", owner, "--ttl", "1h"},
		"adopt with docker context":    {"adopt", treehousepkg.Name, "/pool/3/app", "--lease-id", "tok-A", "--context", "default", "--owner", owner, "--ttl", "1h"},
		"lease id on docker adoption":  {"adopt", "kind", "lab", "--lease-id", "tok-A", "--owner", owner, "--ttl", "1h"},
	} {
		t.Run(name, func(t *testing.T) {
			var result map[string]any
			if err := json.Unmarshal([]byte(drive(t, app, outputpkg.JSON, 2, args...)), &result); err != nil || result["code"] != "VALIDATION_ERROR" {
				t.Fatalf("result = %v, %v", result, err)
			}
		})
	}
}

func TestTreehouseWorktreeOptIn(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DIBS_CONFIG", config)
	t.Setenv("DIBS_STATE", t.TempDir())
	owner := "pid:" + strconv.Itoa(os.Getpid())
	for _, tc := range []struct {
		name, config, env string
		setEnv, want      bool
	}{
		{name: "absent", want: false},
		{name: "config on", config: `{"treehouse_worktree_adapter":true}`, want: true},
		{name: "env on", env: "1", setEnv: true, want: true},
		{name: "env off overrides config", config: `{"treehouse_worktree_adapter":true}`, env: "0", setEnv: true, want: false},
		{name: "invalid env stays off", env: "yes", setEnv: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os.Remove(config)
			if tc.config != "" {
				if err := os.WriteFile(config, []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("BERTH_TREEHOUSE_WORKTREE", "1")
			t.Setenv("DIBS_TREEHOUSE_WORKTREE", tc.env)
			if !tc.setEnv {
				os.Unsetenv("DIBS_TREEHOUSE_WORKTREE")
			}
			resources := newAdapters()
			if got := slices.Contains(resources.CreateNames(), treehousepkg.Name); got != tc.want {
				t.Fatalf("creatable = %v, want %v", got, tc.want)
			}
			for _, command := range []string{"create", "adopt", "release"} {
				if got := strings.Contains(help(command), "treehouse-worktree"); got != tc.want {
					t.Fatalf("%s help shows treehouse-worktree = %v, want %v", command, got, tc.want)
				}
			}
			if _, registered := resources.Lookup(treehousepkg.Name); !registered {
				t.Fatal("recorded leases would become unreadable while the adapter is off")
			}
			if !tc.want {
				for _, args := range [][]string{
					{"create", "--adapter", treehousepkg.Name, "--owner", owner},
					{"adopt", treehousepkg.Name, "/pool/3/app", "--lease-id", "tok-A", "--owner", owner, "--ttl", "1h"},
				} {
					result := invoke(t, 2, args...)
					if msg, _ := result["error"].(string); !strings.Contains(msg, "DIBS_TREEHOUSE_WORKTREE=1") {
						t.Fatalf("disabled adapter error does not name the opt-in: %v", result)
					}
				}
			}
		})
	}
	if err := os.WriteFile(config, []byte(`{"treehouse_worktree_adapter":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if c, err := configpkg.Load(); err != nil || !c.TreehouseWorktree {
		t.Fatalf("strict config load rejected treehouse_worktree_adapter: %+v, %v", c, err)
	}
}

func TestTreehouseContractRefusesUnlandedWork(t *testing.T) {
	t.Setenv("DIBS_STATE", t.TempDir())
	t.Setenv("TREEHOUSE_ROOT", "")
	t.Setenv("DIBS_CONSUMER_CONTRACT", "1")
	// A worktree is host-scoped: a keyed create must not need a Docker engine.
	t.Setenv("DOCKER_HOST", "tcp://docker.invalid:2375")
	fixedBudget(t)
	pool := &fakePool{repo: "/work/app", slot: "/pool/app-1/3/app"}
	app := fakeTreehouseApp(pool)
	owner := "pid:" + strconv.Itoa(os.Getpid())
	var created map[string]any
	if err := json.Unmarshal([]byte(drive(t, app, outputpkg.JSON, 0, "create", "--adapter", treehousepkg.Name, "--repo", "/work/app", "--owner", owner, "--ttl", "1h", "--key", "task-7:worktree")), &created); err != nil {
		t.Fatal(err)
	}
	id := created["lease"].(map[string]any)["id"].(string)
	if created["status"] != "created" || created["contract"] != float64(1) {
		t.Fatalf("keyed create = %v", created)
	}
	if again := drive(t, app, outputpkg.AXI, 0, "create", "--adapter", treehousepkg.Name, "--repo", "/work/app", "--owner", owner, "--ttl", "1h", "--key", "task-7:worktree"); !strings.Contains(again, "status: already_done") || !strings.Contains(again, id) {
		t.Fatalf("keyed retry allocated again:\n%s", again)
	}
	pool.dirty = " M a.txt\n"
	var blocked map[string]any
	if err := json.Unmarshal([]byte(drive(t, app, outputpkg.JSON, 3, "release", id, "--confirm")), &blocked); err != nil {
		t.Fatal(err)
	}
	if blocked["reason"] != "unlanded_work" || blocked["contract"] != float64(1) || blocked["exit_code"] != float64(3) || blocked["blockers"].(map[string]any)["uncommitted_count"] != float64(1) {
		t.Fatalf("contract release refusal = %v", blocked)
	}
	if pool.claim != "tok-A" {
		t.Fatal("refusal returned the slot")
	}
	pool.dirty = ""
	if got := drive(t, app, outputpkg.AXI, 0, "release", id, "--confirm"); !strings.Contains(got, "status: released") {
		t.Fatalf("release = %s", got)
	}
}

func TestTreehouseBlockingProcessesRenderInAXIAndJSON(t *testing.T) {
	t.Setenv("DIBS_STATE", t.TempDir())
	t.Setenv("TREEHOUSE_ROOT", "")
	fixedBudget(t)
	pool := &fakePool{repo: "/work/app", slot: "/pool/app-1/3/app"}
	app := fakeTreehouseApp(pool)
	owner := "pid:" + strconv.Itoa(os.Getpid())
	var created map[string]any
	if err := json.Unmarshal([]byte(drive(t, app, outputpkg.JSON, 0, "create", "--adapter", treehousepkg.Name, "--repo", "/work/app", "--owner", owner, "--ttl", "1h")), &created); err != nil {
		t.Fatal(err)
	}
	id := created["lease"].(map[string]any)["id"].(string)
	pool.processes = []any{map[string]any{"name": "vim", "pid": 4242}}
	axi := drive(t, app, outputpkg.AXI, 1, "release", id, "--confirm")
	if !strings.Contains(axi, "processes[1]{name,pid}:") || !strings.Contains(axi, "vim,4242") {
		t.Fatalf("AXI refusal hides the blocking process:\n%s", axi)
	}
	var blocked map[string]any
	if err := json.Unmarshal([]byte(drive(t, app, outputpkg.JSON, 1, "release", id, "--confirm")), &blocked); err != nil {
		t.Fatal(err)
	}
	if procs := blocked["blockers"].(map[string]any)["processes"].([]any); len(procs) != 1 || procs[0].(map[string]any)["name"] != "vim" || procs[0].(map[string]any)["pid"] != float64(4242) {
		t.Fatalf("JSON blockers = %v", blocked["blockers"])
	}
	pool.processes = nil
	if got := drive(t, app, outputpkg.AXI, 0, "release", id, "--confirm"); !strings.Contains(got, "status: released") {
		t.Fatalf("release after the process exited = %s", got)
	}
}
