//go:build integration

package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	treehousepkg "github.com/Dematom1/dibs/internal/adapter/treehouseworktree"
)

// treehouseLab is a disposable repository with a bare origin and a private
// treehouse pool root, so the test never touches a shared pool.
type treehouseLab struct {
	t                       *testing.T
	repo, root, origin, dir string
}

func newTreehouseLab(t *testing.T) *treehouseLab {
	t.Helper()
	if _, err := exec.LookPath("treehouse"); err != nil {
		t.Fatalf("treehouse executable required for treehouse-worktree integration: %v", err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lab := &treehouseLab{t: t, repo: filepath.Join(dir, "repo"), root: filepath.Join(dir, "pool"), origin: filepath.Join(dir, "origin.git"), dir: dir}
	t.Setenv("DIBS_STATE", filepath.Join(dir, "state"))
	t.Setenv("DIBS_TREEHOUSE_WORKTREE", "1")
	t.Setenv("TREEHOUSE_ROOT", lab.root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := os.WriteFile(filepath.Join(dir, "gitconfig"), []byte("[user]\n\temail = lab@example.invalid\n\tname = lab\n[init]\n\tdefaultBranch = main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lab.git(dir, "init", "-q", "--bare", lab.origin)
	lab.git(dir, "init", "-q", lab.repo)
	if err := os.WriteFile(filepath.Join(lab.repo, "a.txt"), []byte("hi\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lab.git(lab.repo, "add", "a.txt")
	lab.git(lab.repo, "commit", "-qm", "init")
	lab.git(lab.repo, "remote", "add", "origin", lab.origin)
	lab.git(lab.repo, "push", "-q", "-u", "origin", "main")
	lab.git(lab.repo, "remote", "set-head", "origin", "main")
	return lab
}

func (l *treehouseLab) git(dir string, args ...string) string {
	l.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		l.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (l *treehouseLab) treehouse(args ...string) []byte {
	l.t.Helper()
	cmd := exec.Command("treehouse", append(args, "--root", l.root)...)
	cmd.Dir = l.repo
	out, err := cmd.Output()
	if err != nil {
		l.t.Fatalf("treehouse %v: %v", args, err)
	}
	return out
}

type labSlot struct {
	Path        string `json:"path"`
	Status      string `json:"status"`
	LeaseID     string `json:"lease_id"`
	LeaseHolder string `json:"lease_holder"`
}

func (l *treehouseLab) slot(path string) labSlot {
	l.t.Helper()
	var slots []labSlot
	if err := json.Unmarshal(l.treehouse("status", "--json"), &slots); err != nil {
		l.t.Fatal(err)
	}
	for _, s := range slots {
		if s.Path == path {
			return s
		}
	}
	l.t.Fatalf("slot %s not in pool", path)
	return labSlot{}
}

// acquire leases a slot through dibs and returns the lease id and slot path.
func (l *treehouseLab) acquire(owner string, extra ...string) (string, string) {
	l.t.Helper()
	created := invoke(l.t, 0, append([]string{"create", "--adapter", treehousepkg.Name, "--repo", l.repo, "--owner", owner, "--ttl", "1h"}, extra...)...)
	lease := created["lease"].(map[string]any)
	id, path := lease["id"].(string), lease["path"].(string)
	if s := l.slot(path); s.Status != "leased" || s.LeaseHolder != "dibs:"+id || s.LeaseID == "" {
		l.t.Fatalf("slot not durably leased to dibs: %+v", s)
	}
	return id, path
}

func liveProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	return cmd
}

func TestIntegrationTreehouseWorktree(t *testing.T) {
	lab := newTreehouseLab(t)
	owner := "pid:" + strconv.Itoa(os.Getpid())

	t.Run("refuses unlanded work, then returns landed work", func(t *testing.T) {
		gate := filepath.Join(lab.dir, "gate.git")
		lab.git(lab.dir, "init", "-q", "--bare", gate)
		id, path := lab.acquire(owner, "--landed-exclude-remote", "gate")
		if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("work\n"), 0600); err != nil {
			t.Fatal(err)
		}
		blocked := invoke(t, 1, "release", id, "--confirm")
		blockers := blocked["blockers"].(map[string]any)
		if blockers["uncommitted_count"] != float64(1) || blockers["uncommitted"].([]any)[0] != "new.txt" {
			t.Fatalf("dirty refusal = %v", blocked)
		}
		lab.git(path, "add", "new.txt")
		lab.git(path, "commit", "-qm", "work")
		lab.git(path, "remote", "add", "gate", gate)
		lab.git(path, "push", "-q", "gate", "HEAD:refs/heads/work")
		head := lab.git(path, "rev-parse", "HEAD")
		blocked = invoke(t, 1, "release", id, "--confirm")
		if blockers := blocked["blockers"].(map[string]any); blockers["unpushed_count"] != float64(1) || blockers["unpushed"].([]any)[0] != head {
			t.Fatalf("excluded mirror counted as landed: %v", blocked)
		}
		if s := lab.slot(path); s.Status != "leased" {
			t.Fatalf("refusal returned the slot: %+v", s)
		}
		lab.git(path, "push", "-q", "origin", "HEAD:refs/heads/work")
		if got := invoke(t, 0, "release", id, "--confirm"); got["status"] != "released" {
			t.Fatalf("release = %v", got)
		}
		if s := lab.slot(path); s.Status != "available" || s.LeaseID != "" {
			t.Fatalf("slot not returned: %+v", s)
		}
		if got := invoke(t, 0, "release", id, "--confirm"); got["status"] != "already absent (no-op)" {
			t.Fatalf("repeat release = %v", got)
		}
	})

	t.Run("a stale lease never returns a reassigned slot", func(t *testing.T) {
		id, path := lab.acquire(owner)
		lab.treehouse("return", path, "--force")
		var other labSlot
		if err := json.Unmarshal(lab.treehouse("get", "--lease", "--json", "--lease-holder", "other"), &other); err != nil {
			t.Fatal(err)
		}
		if other.Path != path {
			t.Fatalf("pool handed out %s, want the reassigned %s", other.Path, path)
		}
		if err := os.WriteFile(filepath.Join(path, "theirs.txt"), []byte("theirs\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if got := invoke(t, 0, "release", id, "--confirm"); got["status"] != "released" {
			t.Fatalf("stale release = %v", got)
		}
		if s := lab.slot(path); s.LeaseID != other.LeaseID || s.LeaseHolder != "other" {
			t.Fatalf("stale lease touched the new claim: %+v", s)
		}
		if _, err := os.Stat(filepath.Join(path, "theirs.txt")); err != nil {
			t.Fatalf("stale lease reset the new holder's work: %v", err)
		}
		lab.treehouse("return", path, "--force", "--if-lease-id", other.LeaseID)
	})

	t.Run("a crash before recording the token still recovers the slot", func(t *testing.T) {
		id, path := lab.acquire(owner)
		state := filepath.Join(os.Getenv("DIBS_STATE"), "state.json")
		var ledger map[string]any
		data, err := os.ReadFile(state)
		if err == nil {
			err = json.Unmarshal(data, &ledger)
		}
		if err != nil {
			t.Fatal(err)
		}
		resource := ledger["leases"].(map[string]any)[id].(map[string]any)["resource"].(map[string]any)
		delete(resource, "path")
		delete(resource, "treehouse_lease_id")
		delete(resource, "name")
		if data, err = json.Marshal(ledger); err == nil {
			err = os.WriteFile(state, data, 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := invoke(t, 0, "release", id, "--confirm"); got["status"] != "released" {
			t.Fatalf("pending release = %v", got)
		}
		if s := lab.slot(path); s.Status != "available" {
			t.Fatalf("pending claim leaked: %+v", s)
		}
	})

	t.Run("registers an already leased slot and returns only its token", func(t *testing.T) {
		var held labSlot
		if err := json.Unmarshal(lab.treehouse("get", "--lease", "--json", "--lease-holder", "caller"), &held); err != nil {
			t.Fatal(err)
		}
		invoke(t, 1, "adopt", treehousepkg.Name, held.Path, "--lease-id", "not-the-token", "--owner", owner, "--ttl", "1h")
		adopted := invoke(t, 0, "adopt", treehousepkg.Name, held.Path, "--lease-id", held.LeaseID, "--owner", owner, "--ttl", "1h")
		id := adopted["lease"].(map[string]any)["id"].(string)
		invoke(t, 1, "adopt", treehousepkg.Name, held.Path, "--lease-id", held.LeaseID, "--owner", owner, "--ttl", "1h")
		if got := invoke(t, 0, "release", id, "--confirm"); got["status"] != "released" {
			t.Fatalf("adopted release = %v", got)
		}
		if s := lab.slot(held.Path); s.Status != "available" {
			t.Fatalf("adopted slot not returned: %+v", s)
		}
	})

	t.Run("sweep reclaims on proven owner death, never discarding", func(t *testing.T) {
		worker := liveProcess(t)
		workerOwner := "pid:" + strconv.Itoa(worker.Process.Pid)
		clean, cleanPath := lab.acquire(workerOwner)
		dirty, dirtyPath := lab.acquire(workerOwner)
		if err := os.WriteFile(filepath.Join(dirtyPath, "unsaved.txt"), []byte("x\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if preview := invoke(t, 0, "sweep", "--confirm"); len(preview["reclaimed"].([]any)) != 0 {
			t.Fatalf("sweep reclaimed a live owner's slot: %v", preview)
		}
		worker.Process.Kill()
		worker.Wait()
		swept := invoke(t, 1, "sweep", "--confirm")
		reclaimed, failures := swept["reclaimed"].([]any), swept["failures"].(map[string]any)
		if len(reclaimed) != 1 || reclaimed[0] != clean || !strings.Contains(failures[dirty].(string), "unlanded work") {
			t.Fatalf("sweep = %v", swept)
		}
		if s := lab.slot(cleanPath); s.Status != "available" {
			t.Fatalf("dead owner's clean slot not returned: %+v", s)
		}
		if _, err := os.Stat(filepath.Join(dirtyPath, "unsaved.txt")); err != nil {
			t.Fatalf("sweep discarded unlanded work: %v", err)
		}
		if got := invoke(t, 0, "release", dirty, "--confirm", "--discard-unlanded"); got["status"] != "released" {
			t.Fatalf("explicit discard = %v", got)
		}
		if s := lab.slot(dirtyPath); s.Status != "available" {
			t.Fatalf("discarded slot not returned: %+v", s)
		}
		if _, err := os.Stat(filepath.Join(dirtyPath, "unsaved.txt")); !os.IsNotExist(err) {
			t.Fatalf("discard kept the file: %v", err)
		}
	})

	t.Run("slow returns do not stall other leases' renewals", func(t *testing.T) {
		// Each return waits out treehouse's process grace period; renewals must not queue behind them.
		var ids []string
		for range 3 {
			id, path := lab.acquire(owner)
			sleeper := exec.Command("sleep", "30")
			sleeper.Dir = path
			if err := sleeper.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sleeper.Process.Kill(); sleeper.Wait() })
			ids = append(ids, id)
		}
		renewed := invoke(t, 0, "create", "--owner", owner, "--ttl", "1h")["lease"].(map[string]any)["id"].(string)
		done := make(chan time.Duration, len(ids))
		for _, id := range ids {
			go func() {
				start := time.Now()
				var out strings.Builder
				run(context.Background(), []string{"--json", "release", id, "--confirm"}, &out)
				done <- time.Since(start)
			}()
		}
		time.Sleep(300 * time.Millisecond)
		start := time.Now()
		invoke(t, 0, "extend", renewed, "--ttl", "2h")
		renewal := time.Since(start)
		var slowest time.Duration
		for range ids {
			slowest = max(slowest, <-done)
		}
		t.Logf("renewal %v while %d slot returns ran (slowest return %v)", renewal, len(ids), slowest)
		if renewal > slowest/2 {
			t.Fatalf("renewal took %v, stalled behind returns of up to %v", renewal, slowest)
		}
		invoke(t, 0, "release", renewed, "--confirm")
	})
}
