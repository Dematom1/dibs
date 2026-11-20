package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	composepkg "github.com/Dematom1/dibs/internal/adapter/compose"
	configpkg "github.com/Dematom1/dibs/internal/config"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	policypkg "github.com/Dematom1/dibs/internal/policy"
)

// orphanAuditOff names the settings that allow dibs orphans --adopt.
const orphanAuditOff = `orphan adoption is off; set DIBS_ORPHAN_AUDIT=1 or "orphan_audit": true in the user config`

func orphanAuditEnabled() bool { return optedIn("DIBS_ORPHAN_AUDIT", configpkg.OrphanAudit) }

// defaultEvidenceRoots match one agent slot each: a treehouse pool slot and a no-mistakes run worktree.
var defaultEvidenceRoots = []string{"~/.treehouse/*/*", "~/.no-mistakes/worktrees/*/*"}

// Audit actions are stable output values.
const (
	auditAdopted     = "adopted"
	auditSkippedLive = "skipped_live"
	auditReportOnly  = "report_only"
	auditFailed      = "failed"
)

// evidenceRoots returns the absolute slot patterns that mark agent work.
func evidenceRoots() ([]string, error) {
	roots := configpkg.OrphanEvidenceRoots()
	if roots == nil {
		roots = defaultEvidenceRoots
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	expanded := make([]string, 0, len(roots))
	for _, root := range roots {
		if rest, ok := strings.CutPrefix(root, "~/"); ok {
			root = filepath.Join(home, rest)
		}
		if _, err := filepath.Match(root, ""); err != nil || !filepath.IsAbs(root) || filepath.Clean(root) == "/" {
			return nil, errors.New(`orphan_evidence_roots entries must be absolute or ~/ glob patterns below /, such as "~/.treehouse/*/*"`)
		}
		expanded = append(expanded, filepath.Clean(root))
	}
	return expanded, nil
}

// slotOf returns the agent slot holding path: its leading components that match a root pattern.
func slotOf(path string, roots []string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	parts := strings.Split(filepath.Clean(path), "/")
	for _, root := range roots {
		n := strings.Count(root, "/") + 1
		if len(parts) < n {
			continue
		}
		if prefix := strings.Join(parts[:n], "/"); prefix != "" {
			if ok, _ := filepath.Match(root, prefix); ok {
				return prefix, true
			}
		}
	}
	return "", false
}

// processDirs lists the working directories of this user's processes; an error means liveness is unknown.
func processDirs(ctx context.Context, run adapterpkg.CommandRunner) ([]string, error) {
	out, err := run(ctx, nil, "lsof", "-nP", "-a", "-u", strconv.Itoa(os.Getuid()), "-d", "cwd", "-Fn")
	if err != nil {
		return nil, errors.New("cannot list process working directories with lsof; liveness unknown")
	}
	var dirs []string
	for line := range strings.Lines(string(out)) {
		if dir, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "n"); ok && filepath.IsAbs(dir) {
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == 0 {
		return nil, errors.New("lsof listed no process working directories; liveness unknown")
	}
	return dirs, nil
}

// classify decides what the audit may do with one orphan row. Only a Compose
// project whose every working directory sits in an agent slot, or that carries
// an io.dibs.task label, is adoptable, and only while no process works there.
// dirs is nil when process liveness is unknown.
func classify(row map[string]any, roots, dirs []string) (string, string) {
	if row["type"] != "compose" {
		return auditReportOnly, "only Compose projects are adopted"
	}
	var workdirs []string
	task := false
	evidence, _ := row["evidence"].(map[string]any)
	for _, value := range evidence {
		object, _ := value.(map[string]any)
		labels, _ := object["labels"].(map[string]string)
		if labels["io.dibs.lease"] != "" {
			return auditReportOnly, "labeled for a dibs lease this state does not hold; inspect it on its own machine or state"
		}
		task = task || labels["io.dibs.task"] != ""
		if dir := labels["com.docker.compose.project.working_dir"]; dir != "" && !slices.Contains(workdirs, dir) {
			workdirs = append(workdirs, dir)
		}
	}
	var places []string
	for _, dir := range workdirs {
		slot, ok := slotOf(dir, roots)
		if !ok && !task {
			return auditReportOnly, "no agent evidence: " + dir + " is outside the evidence roots"
		}
		if !ok {
			slot = dir
		}
		places = append(places, slot)
	}
	if len(places) == 0 && !task {
		return auditReportOnly, "no agent evidence: no working directory or io.dibs.task label"
	}
	if dirs == nil {
		return auditSkippedLive, "process liveness unknown; nothing is adopted"
	}
	for _, place := range places {
		for _, dir := range dirs {
			if dir == place || strings.HasPrefix(dir, place+"/") {
				return auditSkippedLive, "a live process works in " + place
			}
		}
	}
	return auditAdopted, ""
}

// adoptOrphans adopts each agent-evidenced Compose orphan for this short-lived
// process, so its owner is dead as soon as the audit exits and the normal sweep
// reclaims it after any policy grace. It annotates every row and returns the counts.
func (app application) adoptOrphans(ctx context.Context, docker adapterpkg.ResourceRef, rows []map[string]any, ttl time.Duration) (map[string]int, error) {
	roots, err := evidenceRoots()
	if err != nil {
		return nil, err
	}
	dirs, liveErr := processDirs(ctx, adapterpkg.RunCommand)
	if liveErr != nil {
		dirs = nil
	}
	owner, err := app.owners.Bind(ctx, "pid:"+strconv.Itoa(os.Getpid()), time.Minute, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	policy, ttl, err := app.leasePolicy("compose", owner.Kind, ttl, true, 0, 0)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{"found": len(rows), auditAdopted: 0, auditSkippedLive: 0, auditReportOnly: 0, auditFailed: 0}
	for _, row := range rows {
		action, reason := classify(row, roots, dirs)
		if action == auditSkippedLive && liveErr != nil {
			reason = liveErr.Error()
		}
		audit := map[string]any{"action": action}
		if action == auditAdopted {
			id, err := app.adoptOrphan(ctx, docker, row, owner, ttl, policy)
			if err != nil {
				action, reason = auditFailed, err.Error()
				audit["action"] = action
			} else {
				audit["lease"] = id
			}
		}
		if reason != "" {
			audit["reason"] = reason
		}
		row["audit"] = audit
		counts[action]++
	}
	return counts, nil
}

func (app application) adoptOrphan(ctx context.Context, docker adapterpkg.ResourceRef, row map[string]any, owner ownerpkg.Reference, ttl time.Duration, policy *policypkg.Policy) (string, error) {
	data, err := composepkg.FromLabels(row)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "dibs-orphan-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	docker.Adapter, docker.Name = "compose", row["name"].(string)
	l, _, err := app.leases.Adopt(ctx, leasepkg.AdoptRequest{Owner: owner, TTL: ttl, Resource: docker, File: file.Name(), Policy: policy})
	if err != nil {
		return "", err
	}
	return l.ID, nil
}

// orbstackMachines lists OrbStack machines as report-only rows; no adapter leases them yet.
func orbstackMachines(ctx context.Context) ([]map[string]any, error) {
	if _, err := exec.LookPath("orbctl"); err != nil {
		return nil, nil
	}
	out, err := adapterpkg.RunCommand(ctx, nil, "orbctl", "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	var machines []struct{ Name, State string }
	if err := json.Unmarshal(out, &machines); err != nil {
		return nil, errors.New("cannot decode orbctl list --format json")
	}
	rows := []map[string]any{}
	for _, m := range machines {
		rows = append(rows, map[string]any{"type": "orbstack-machine", "name": m.Name, "server": "orbstack", "state": m.State})
	}
	return rows, nil
}
