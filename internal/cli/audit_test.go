package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func composeRow(labels ...map[string]string) map[string]any {
	evidence := map[string]any{}
	for i, l := range labels {
		evidence["container:c"+string(rune('0'+i))] = map[string]any{"labels": l, "image": "alpine"}
	}
	return map[string]any{"type": "compose", "name": "lab", "evidence": evidence}
}

func workdir(dir string) map[string]string {
	return map[string]string{"com.docker.compose.project.working_dir": dir, "com.docker.compose.service": "web"}
}

func TestClassifyAdoptsOnlyIdleAgentEvidence(t *testing.T) {
	roots := []string{"/h/.treehouse/*/*", "/h/.no-mistakes/worktrees/*/*"}
	idle := []string{"/", "/h/Code/app"}
	for _, c := range []struct {
		name   string
		row    map[string]any
		dirs   []string
		action string
	}{
		{"treehouse slot", composeRow(workdir("/h/.treehouse/pool/3/repo/demo")), idle, auditAdopted},
		{"no-mistakes worktree, file gone", composeRow(workdir("/h/.no-mistakes/worktrees/r/run")), idle, auditAdopted},
		{"task label outside roots", composeRow(map[string]string{"io.dibs.task": "t1", "com.docker.compose.project.working_dir": "/h/Code/other"}), idle, auditAdopted},
		{"human project", composeRow(workdir("/h/Code/app")), idle, auditReportOnly},
		{"pool root itself is not a slot", composeRow(workdir("/h/.treehouse/pool")), idle, auditReportOnly},
		{"mixed agent and human dirs", composeRow(workdir("/h/.treehouse/pool/3/repo"), workdir("/h/Code/app")), idle, auditReportOnly},
		{"no evidence at all", composeRow(map[string]string{"com.docker.compose.service": "web"}), idle, auditReportOnly},
		{"another state's lease", composeRow(map[string]string{"io.dibs.lease": "x", "io.dibs.task": "t"}), idle, auditReportOnly},
		{"kind cluster", map[string]any{"type": "kind", "name": "lab"}, idle, auditReportOnly},
		{"live process in slot", composeRow(workdir("/h/.treehouse/pool/3/repo/demo")), []string{"/h/.treehouse/pool/3/repo"}, auditSkippedLive},
		{"live process at task dir", composeRow(map[string]string{"io.dibs.task": "t1", "com.docker.compose.project.working_dir": "/h/Code/other"}), []string{"/h/Code/other/sub"}, auditSkippedLive},
		{"sibling slot prefix is not live", composeRow(workdir("/h/.treehouse/pool/3/repo")), []string{"/h/.treehouse/pool/30/repo"}, auditAdopted},
		{"liveness unknown", composeRow(workdir("/h/.treehouse/pool/3/repo")), nil, auditSkippedLive},
	} {
		if action, reason := classify(c.row, roots, c.dirs); action != c.action {
			t.Errorf("%s: got %s (%s), want %s", c.name, action, reason, c.action)
		}
	}
}

func TestEvidenceRootsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DIBS_CONFIG", config)
	roots, err := evidenceRoots()
	if err != nil || len(roots) != 2 || roots[0] != filepath.Join(home, ".treehouse/*/*") {
		t.Fatalf("defaults: %v %v", roots, err)
	}
	for body, ok := range map[string]bool{`{"orphan_evidence_roots": ["/work/*"]}`: true, `{"orphan_evidence_roots": ["relative/*"]}`: false, `{"orphan_evidence_roots": ["/"]}`: false, `{"orphan_evidence_roots": ["/w/["]}`: false} {
		if err := os.WriteFile(config, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := evidenceRoots(); (err == nil) != ok {
			t.Errorf("%s: %v", body, err)
		}
	}
}

func TestOrphanAdoptIsOptInAndNeedsTTL(t *testing.T) {
	t.Setenv("DIBS_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	for _, c := range []struct {
		optIn string
		args  []string
		want  string
	}{
		{"", []string{"orphans", "--adopt", "--ttl", "30m"}, "DIBS_ORPHAN_AUDIT"},
		{"1", []string{"orphans", "--adopt"}, "positive --ttl"},
		{"1", []string{"orphans", "--ttl", "30m"}, "only with --adopt"},
		{"1", []string{"orphans", "--adopt", "--ttl", "-1m"}, "positive --ttl"},
	} {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			if c.optIn != "" {
				t.Setenv("DIBS_ORPHAN_AUDIT", c.optIn)
			}
			dir := filepath.Join(t.TempDir(), "untouched")
			t.Setenv("DIBS_STATE", dir)
			t.Setenv("PATH", "")
			var out bytes.Buffer
			if code := run(context.Background(), append([]string{"--json"}, c.args...), &out); code != 2 || !strings.Contains(out.String(), c.want) {
				t.Fatalf("code %d: %s", code, out.String())
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("usage error touched state")
			}
		})
	}
}
