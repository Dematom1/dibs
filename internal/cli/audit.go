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

// classify reads one orphan row's agent evidence. Only a Compose project whose
// every working directory sits in an agent slot, or that carries an io.dibs.task
// label, is a candidate; places are the slots, or the working directories of a
// label-only match, where a live process means the project is still in use.
func classify(row map[string]any, roots []string) (action, reason string, places []string) {
	if row["type"] == "orbstack-machine" {
		return auditReportOnly, "no dibs adapter manages OrbStack machines; reported only, never adopted", nil
	}
	if row["type"] != "compose" {
		return auditReportOnly, "only Compose projects are adopted", nil
	}
	var workdirs []string
	task, unplaced := false, ""
	evidence, _ := row["evidence"].(map[string]any)
	for key, value := range evidence {
		object, _ := value.(map[string]any)
		labels, _ := object["labels"].(map[string]string)
		if labels["io.dibs.lease"] != "" {
			return auditReportOnly, "labeled for a dibs lease this state does not hold; inspect it on its own machine or state", nil
		}
		task = task || labels["io.dibs.task"] != ""
		dir := labels["com.docker.compose.project.working_dir"]
		if container := strings.HasPrefix(key, "container:"); container && dir == "" {
			// Every container's work must be observable; one without a directory cannot be checked for live work.
			unplaced = key
		} else if dir != "" && !filepath.IsAbs(dir) {
			return auditReportOnly, "working directory " + dir + " is not absolute; it cannot be checked for live work", nil
		} else if dir != "" && !slices.Contains(workdirs, dir) {
			workdirs = append(workdirs, dir)
		}
	}
	if unplaced != "" && !task {
		return auditReportOnly, "no agent evidence: " + unplaced + " has no working directory", nil
	}
	if unplaced != "" {
		return auditSkippedLive, unplaced + " has no working directory to check for live work; nothing is adopted", nil
	}
	for _, dir := range workdirs {
		slot, ok := slotOf(dir, roots)
		if !ok && !task {
			return auditReportOnly, "no agent evidence: " + dir + " is outside the evidence roots", nil
		}
		if !ok {
			slot = filepath.Clean(dir)
		}
		if !slices.Contains(places, slot) {
			places = append(places, slot)
		}
	}
	if len(places) == 0 && !task {
		return auditReportOnly, "no agent evidence: no working directory or io.dibs.task label", nil
	}
	// Without a working directory there is nowhere to look for live work, so a task label alone never adopts.
	if len(places) == 0 {
		return auditSkippedLive, "io.dibs.task label but no working directory to check for live work; nothing is adopted", nil
	}
	return auditAdopted, "", places
}

// adoptOrphans adopts each agent-evidenced Compose orphan for this short-lived
// process, so its owner is dead as soon as the audit exits and the normal sweep
// reclaims it after any policy grace. Liveness is read again for each project
// right before it is adopted, and the lease watches the same places, so sweep
// holds it while a live process works there. It annotates every row and returns the counts.
func (app application) adoptOrphans(ctx context.Context, docker adapterpkg.ResourceRef, rows []map[string]any, ttl time.Duration) (map[string]int, error) {
	roots, err := evidenceRoots()
	if err != nil {
		return nil, err
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
	// lsof sees only this host's processes, so only a local engine's projects can be checked for live work.
	local := strings.HasPrefix(docker.DockerHost, "unix://")
	for _, row := range rows {
		action, reason, places := classify(row, roots)
		if action == auditAdopted && !local {
			action, reason = auditReportOnly, "Docker endpoint "+docker.DockerHost+" is not a local socket; live work there cannot be observed from this host"
		}
		audit := map[string]any{}
		if action == auditAdopted {
			if activity, err := adapterpkg.LiveWork(ctx, adapterpkg.RunCommand, places); err != nil {
				action, reason = auditSkippedLive, err.Error()+"; nothing is adopted"
			} else if activity.Busy != "" {
				action, reason = auditSkippedLive, activity.Busy
			} else if id, err := app.adoptOrphan(ctx, docker, row, places, owner, ttl, policy); err != nil {
				action, reason = auditFailed, err.Error()
			} else {
				audit["lease"] = id
			}
		}
		audit["action"] = action
		if reason != "" {
			audit["reason"] = reason
		}
		row["audit"] = audit
		counts[action]++
	}
	return counts, nil
}

func (app application) adoptOrphan(ctx context.Context, docker adapterpkg.ResourceRef, row map[string]any, watch []string, owner ownerpkg.Reference, ttl time.Duration, policy *policypkg.Policy) (string, error) {
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
	docker.Adapter, docker.Name, docker.Watch = "compose", row["name"].(string), watch
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
		rows = append(rows, map[string]any{"type": "orbstack-machine", "name": m.Name, "server": "orbstack", "state": m.State, "managed": false})
	}
	return rows, nil
}
