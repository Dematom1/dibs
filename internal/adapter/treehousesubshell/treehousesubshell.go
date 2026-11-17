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

// holder reports whether a command line is a treehouse process that opened a
// subshell: treehouse get, treehouse enter, or bare treehouse, an alias for get.
func holder(command string) bool {
	fields := strings.Fields(command)
	return len(fields) >= 1 && filepath.Base(fields[0]) == "treehouse" && (len(fields) == 1 || fields[1] == "get" || fields[1] == "enter")
}

func directChildren(all []entry, parent int) []entry {
	children := []entry{}
	for _, e := range all {
		if e.ppid == parent {
			children = append(children, e)
		}
	}
	return children
}

func list(ctx context.Context, sys terminalpkg.System, _ string) ([]terminalpkg.Pane, error) {
	all, err := processes(ctx, sys, "-ax")
	if err != nil {
		return nil, err
	}
	panes, subshells := []terminalpkg.Pane{}, []string{}
	for _, e := range all {
		if !holder(e.command) {
			continue
		}
		children := directChildren(all, e.pid)
		pane := terminalpkg.Pane{ID: strconv.Itoa(e.pid), PID: e.pid, Label: e.command}
		for _, child := range children {
			pane.ChildPIDs = append(pane.ChildPIDs, child.pid)
			pane.Command = join(pane.Command, child.command)
			subshells = append(subshells, strconv.Itoa(child.pid))
		}
		panes = append(panes, pane)
	}
	dirs := workingDirs(ctx, sys, subshells)
	for i := range panes {
		for _, pid := range panes[i].ChildPIDs {
			panes[i].Dir = join(panes[i].Dir, dirs[pid])
		}
	}
	return panes, nil
}

func join(current, next string) string {
	if next == "" {
		return current
	}
	if current == "" {
		return next
	}
	return current + ", " + next
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
	all, err := processes(ctx, sys, "-ax")
	if err != nil {
		return terminalpkg.Pane{}, false, err
	}
	for _, e := range all {
		if strconv.Itoa(e.pid) != id || !holder(e.command) {
			continue
		}
		p := terminalpkg.Pane{ID: id, PID: e.pid, Label: e.command}
		for _, child := range directChildren(all, e.pid) {
			p.ChildPIDs = append(p.ChildPIDs, child.pid)
			p.Command = join(p.Command, child.command)
		}
		return p, true, nil
	}
	return terminalpkg.Pane{}, false, nil
}

// hangup sends SIGHUP to the subshells the treehouse process holds, not to treehouse itself.
func hangup(_ context.Context, sys terminalpkg.System, p terminalpkg.Pane) {
	for _, pid := range p.ChildPIDs {
		_ = sys.Signal(pid, syscall.SIGHUP)
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
