package treehousesubshell

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	terminalpkg "github.com/Dematom1/dibs/internal/adapter/terminal"
)

// A treehouse subshell is local: its ID is the PID of the treehouse get or enter process that holds the subshell.
const local = "local"

var processID = regexp.MustCompile(`^[1-9][0-9]*$`)

func resolveServer(_ context.Context, _ terminalpkg.System, requested string) (string, error) {
	if requested != "" && requested != local {
		return "", errors.New("treehouse subshells are local; omit --server")
	}
	return local, nil
}

type entry struct {
	pid, ppid int
	command   string
}

func processes(ctx context.Context, sys terminalpkg.System, args ...string) ([]entry, error) {
	out, stderr, code, err := sys.Run(ctx, []string{"LC_ALL=C"}, "ps", append(args, "-ww", "-o", "pid=,ppid=,command=")...)
	if err != nil {
		return nil, err
	}
	// ps -p exits 1 with no output at all when no listed process exists.
	if code == 1 && len(out) == 0 && strings.TrimSpace(stderr) == "" {
		return nil, nil
	}
	if code != 0 {
		return nil, fmt.Errorf("ps exited %d: %s", code, strings.TrimSpace(stderr))
	}
	entries := []entry{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 3 {
			return nil, errors.New("malformed process listing; existence is unknown")
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			return nil, errors.New("malformed process listing; existence is unknown")
		}
		entries = append(entries, entry{pid, ppid, strings.Join(fields[2:], " ")})
	}
	return entries, nil
}

// holder reports whether a command line is a treehouse process that opened a subshell.
func holder(command string) bool {
	fields := strings.Fields(command)
	return len(fields) >= 2 && filepath.Base(fields[0]) == "treehouse" && (fields[1] == "get" || fields[1] == "enter")
}

func list(ctx context.Context, sys terminalpkg.System, _ string) ([]terminalpkg.Pane, error) {
	all, err := processes(ctx, sys, "-ax")
	if err != nil {
		return nil, err
	}
	shells := map[int]entry{}
	for _, e := range all {
		if _, seen := shells[e.ppid]; !seen {
			shells[e.ppid] = e
		}
	}
	panes, subshells := []terminalpkg.Pane{}, []string{}
	for _, e := range all {
		if holder(e.command) {
			panes = append(panes, terminalpkg.Pane{ID: strconv.Itoa(e.pid), PID: e.pid, Label: e.command, Command: shells[e.pid].command})
			if shell, ok := shells[e.pid]; ok {
				subshells = append(subshells, strconv.Itoa(shell.pid))
			}
		}
	}
	dirs := workingDirs(ctx, sys, subshells)
	for i := range panes {
		panes[i].Dir = dirs[shells[panes[i].PID].pid]
	}
	return panes, nil
}

// workingDirs reads each subshell's cwd, the worktree it works in. It is
// evidence for a human choosing what to adopt, so a failed read leaves it blank.
func workingDirs(ctx context.Context, sys terminalpkg.System, pids []string) map[int]string {
	dirs := map[int]string{}
	if len(pids) == 0 {
		return dirs
	}
	out, _, _, err := sys.Run(ctx, nil, "lsof", "-a", "-d", "cwd", "-Fpn", "-p", strings.Join(pids, ","))
	if err != nil {
		return dirs
	}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && pid > 0:
			dirs[pid] = line[1:]
		}
	}
	return dirs
}

func lookup(ctx context.Context, sys terminalpkg.System, _ string, id string) (terminalpkg.Pane, bool, error) {
	found, err := processes(ctx, sys, "-p", id)
	if err != nil {
		return terminalpkg.Pane{}, false, err
	}
	for _, e := range found {
		if strconv.Itoa(e.pid) == id && holder(e.command) {
			return terminalpkg.Pane{ID: id, PID: e.pid, Label: e.command}, true, nil
		}
	}
	return terminalpkg.Pane{}, false, nil
}

// hangup sends SIGHUP to the subshells the treehouse process holds, not to treehouse itself.
func hangup(ctx context.Context, sys terminalpkg.System, p terminalpkg.Pane) {
	all, err := processes(ctx, sys, "-ax")
	if err != nil {
		return
	}
	for _, e := range all {
		if e.ppid == p.PID {
			_ = sys.Signal(e.pid, syscall.SIGHUP)
		}
	}
}

func closeSubshell(_ context.Context, sys terminalpkg.System, _ string, p terminalpkg.Pane) error {
	if err := sys.Signal(p.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func New(sys terminalpkg.System) adapterpkg.Definition {
	return terminalpkg.New(terminalpkg.Driver{Name: "treehouse-subshell", Noun: "subshell", ValidID: processID.MatchString, ResolveServer: resolveServer, List: list, Lookup: lookup, Hangup: hangup, Close: closeSubshell}, sys)
}
