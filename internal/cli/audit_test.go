package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// The orphan-audit acceptance contract adopts Compose projects only from agent slots or task labels; human projects and non-Compose resources remain report-only.
func TestClassifyAdoptsOnlyAgentEvidence(t *testing.T) {
	roots := []string{"/h/.treehouse/*/*", "/h/.no-mistakes/worktrees/*/*"}
	for _, c := range []struct {
		name   string
		row    map[string]any
		action string
		places []string
	}{
		{"treehouse slot", composeRow(workdir("/h/.treehouse/pool/3/repo/demo")), auditAdopted, []string{"/h/.treehouse/pool/3"}},
		{"no-mistakes worktree, file gone", composeRow(workdir("/h/.no-mistakes/worktrees/r/run")), auditAdopted, []string{"/h/.no-mistakes/worktrees/r/run"}},
		{"two dirs in one slot", composeRow(workdir("/h/.treehouse/pool/3/a"), workdir("/h/.treehouse/pool/3/b")), auditAdopted, []string{"/h/.treehouse/pool/3"}},
		{"task label outside roots", composeRow(map[string]string{"io.dibs.task": "t1", "com.docker.compose.project.working_dir": "/h/Code/other"}), auditAdopted, []string{"/h/Code/other"}},
		{"human project", composeRow(workdir("/h/Code/app")), auditReportOnly, nil},
		{"pool root itself is not a slot", composeRow(workdir("/h/.treehouse/pool")), auditReportOnly, nil},
		{"mixed agent and human dirs", composeRow(workdir("/h/.treehouse/pool/3/repo"), workdir("/h/Code/app")), auditReportOnly, nil},
		{"task label without a working directory", composeRow(map[string]string{"io.dibs.task": "t1", "com.docker.compose.service": "web"}), auditSkippedLive, nil},
		{"no evidence at all", composeRow(map[string]string{"com.docker.compose.service": "web"}), auditReportOnly, nil},
		{"another state's lease", composeRow(map[string]string{"io.dibs.lease": "x", "io.dibs.task": "t"}), auditReportOnly, nil},
		{"kind cluster", map[string]any{"type": "kind", "name": "lab"}, auditReportOnly, nil},
		{"OrbStack machine", map[string]any{"type": "orbstack-machine", "name": "dev"}, auditReportOnly, nil},
	} {
		action, reason, places := classify(c.row, roots)
		if action != c.action || !slices.Equal(places, c.places) {
			t.Errorf("%s: got %s %v (%s), want %s %v", c.name, action, places, reason, c.action, c.places)
		}
		if c.name == "OrbStack machine" && reason != "no dibs adapter manages OrbStack machines; reported only, never adopted" {
			t.Errorf("OrbStack reason = %q", reason)
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
		{"0", []string{"orphans", "--adopt", "--ttl", "30m"}, "DIBS_ORPHAN_AUDIT"},
		{"1", []string{"orphans", "--adopt"}, "positive --ttl"},
		{"1", []string{"orphans", "--ttl", "30m"}, "only with --adopt"},
		{"1", []string{"orphans", "--adopt", "--ttl", "-1m"}, "positive --ttl"},
	} {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			t.Setenv("DIBS_ORPHAN_AUDIT", c.optIn)
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
