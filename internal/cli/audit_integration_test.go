//go:build integration

package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
)

// TestIntegrationOrphanAudit runs the audit as its own process, as the sweep job
// does, so the adopted orphans' owner is really gone when sweep runs.
func TestIntegrationOrphanAudit(t *testing.T) {
	integrationReady(t, "compose")
	t.Setenv("DOCKER_HOST", "")
	ctx := context.Background()
	docker, err := adapterpkg.ResolveDockerContext(ctx, "", adapterpkg.RunCommand)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", binary, moduleRoot(t)).CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	root := t.TempDir()
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, fmt.Appendf(nil, `{"orphan_audit": true, "orphan_evidence_roots": [%q]}`, filepath.Join(root, "pool", "*")), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_CONFIG", config)
	var token [6]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("audit%x", token)
	compose := "services:\n  web:\n    image: alpine:3.23\n    command: [sleep, '600']\n    volumes: [data:/data]\n    networks: [back]\nvolumes:\n  data: {}\nnetworks:\n  back: {}\n"
	up := func(name, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			// The adopted project is gone already; the other two are this test's own fixtures.
			for _, kind := range []string{"container", "volume", "network"} {
				args := []string{"--context", docker.Context, kind, "ls", "-q", "--filter", "label=com.docker.compose.project=" + name}
				if kind == "container" {
					args = append(args, "-a")
				}
				out, _ := adapterpkg.RunCommand(ctx, nil, "docker", args...)
				for _, id := range strings.Fields(string(out)) {
					adapterpkg.RunCommand(ctx, nil, "docker", "--context", docker.Context, kind, "rm", "-f", id)
				}
			}
		})
		cmd := exec.Command("docker", "--context", docker.Context, "compose", "-p", name, "up", "-d")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("compose up %s: %s %v", name, out, err)
		}
	}
	adopted, live, human := prefix+"-adopted", prefix+"-live", prefix+"-human"
	up(adopted, filepath.Join(root, "pool", "1", "app"))
	up(live, filepath.Join(root, "pool", "2", "app"))
	up(human, filepath.Join(root, "human", "app"))
	// The worktree was reused: the adopted project's compose file is gone.
	if err := os.RemoveAll(filepath.Join(root, "pool", "1")); err != nil {
		t.Fatal(err)
	}
	worker := exec.Command("sleep", "600")
	worker.Dir = filepath.Join(root, "pool", "2", "app")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { worker.Process.Kill(); worker.Wait() })

	// The audit scans the whole Docker context; refuse to run where it could adopt a project that is not a fixture.
	for _, item := range invoke(t, 0, "orphans", "--context", docker.Context)["orphans"].([]any) {
		row := item.(map[string]any)
		if name := row["name"].(string); !strings.HasPrefix(name, prefix) && strings.Contains(fmt.Sprint(row["evidence"]), "io.dibs.task:") {
			t.Fatalf("Docker context %s holds unleased project %s with an io.dibs.task label that this test would adopt; run it on an engine without one", docker.Context, name)
		}
	}
	out, err := exec.Command(binary, "--json", "orphans", "--context", docker.Context, "--adopt", "--ttl", "30m").Output()
	if err != nil {
		t.Fatalf("orphans --adopt: %s %v", out, err)
	}
	var result struct {
		Audit   map[string]int
		Orphans []map[string]any
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("not JSON: %s", out)
	}
	actions := map[string]string{}
	var lease string
	for _, row := range result.Orphans {
		audit := row["audit"].(map[string]any)
		actions[row["name"].(string)] = audit["action"].(string)
		if row["name"] == adopted {
			lease, _ = audit["lease"].(string)
		}
	}
	if actions[adopted] != auditAdopted || actions[live] != auditSkippedLive || actions[human] != auditReportOnly || lease == "" {
		t.Fatalf("audit actions %v: %s", actions, out)
	}
	if result.Audit["found"] != len(result.Orphans) || result.Audit[auditAdopted] < 1 || result.Audit[auditSkippedLive] < 1 || result.Audit[auditReportOnly] < 1 {
		t.Fatalf("audit counts %v", result.Audit)
	}
	// The slot is reused before the sweep: new live work there holds the adopted project.
	reused := filepath.Join(root, "pool", "1", "app")
	if err := os.MkdirAll(reused, 0700); err != nil {
		t.Fatal(err)
	}
	agent := exec.Command("sleep", "600")
	agent.Dir = reused
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.Process.Kill(); agent.Wait() })
	held := invoke(t, 0, "sweep", "--confirm")
	holds, _ := held["held"].(map[string]any)
	reason, _ := holds[lease].(string)
	if len(held["reclaimed"].([]any)) != 0 || reason != string(leasepkg.HoldResourceBusy) {
		t.Fatalf("sweep must hold the adopted project as resource_busy while its slot has live work: %v", held)
	}
	if stillThere, err := adapterpkg.RunCommand(ctx, nil, "docker", "--context", docker.Context, "volume", "ls", "-q", "--filter", "label=com.docker.compose.project="+adopted); err != nil || len(strings.Fields(string(stillThere))) == 0 {
		t.Fatalf("held project lost its volume: %s %v", stillThere, err)
	}
	agent.Process.Kill()
	agent.Wait()
	reclaimed := invoke(t, 0, "sweep", "--confirm")["reclaimed"].([]any)
	if len(reclaimed) != 1 || reclaimed[0] != lease {
		t.Fatalf("sweep did not reclaim the dead-owned orphan %s: %v", lease, reclaimed)
	}
	for _, kind := range []string{"volume", "network"} {
		out, err := adapterpkg.RunCommand(ctx, nil, "docker", "--context", docker.Context, kind, "ls", "-q", "--filter", "label=com.docker.compose.project="+adopted)
		if err != nil || len(strings.Fields(string(out))) != 0 {
			t.Fatalf("adopted %s survived sweep: %s %v", kind, out, err)
		}
	}
	remaining := invoke(t, 0, "orphans", "--context", docker.Context)
	names := map[string]bool{}
	for _, item := range remaining["orphans"].([]any) {
		names[item.(map[string]any)["name"].(string)] = true
	}
	if names[adopted] || !names[live] || !names[human] {
		t.Fatalf("sweep must remove only the adopted orphan: %v", names)
	}
}
