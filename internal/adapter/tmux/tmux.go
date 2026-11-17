package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	terminalpkg "github.com/Dematom1/dibs/internal/adapter/terminal"
)

var paneID = regexp.MustCompile(`^%[0-9]+$`)

// identity holds only fields tmux formats without free text, so a pane's cwd or name can never forge a record.
const identity = "#{pane_id} #{pane_pid}"

// display carries free text for people only; the cwd goes last, and its lines never decide identity.
const display = "#{pane_id}|#{pane_current_command}|#{session_name}:#{window_index}.#{pane_index}|#{pane_current_path}"

// resolveServer pins the socket path: an explicit path, else the caller's $TMUX socket, else tmux's default socket.
func resolveServer(_ context.Context, _ terminalpkg.System, requested string) (string, error) {
	socket := requested
	if socket == "" {
		socket, _, _ = strings.Cut(os.Getenv("TMUX"), ",")
	}
	if socket == "" {
		dir := os.Getenv("TMUX_TMPDIR")
		if dir == "" {
			dir = "/tmp"
		}
		socket = filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), "default")
	}
	if !filepath.IsAbs(socket) {
		return "", errors.New("tmux server must be an absolute socket path")
	}
	return canonical(filepath.Clean(socket)), nil
}

// canonical resolves symlinks in the nearest existing ancestor, so /tmp and /private/tmp pin the same server before and after the socket exists.
func canonical(path string) string {
	rest := ""
	for dir := path; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		if dir == filepath.Dir(dir) {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

func listPanes(ctx context.Context, sys terminalpkg.System, server, format string) (string, error) {
	out, stderr, code, err := sys.Run(ctx, nil, "tmux", "-S", server, "list-panes", "-a", "-F", format)
	if err != nil {
		return "", err
	}
	if code != 0 {
		// tmux reports a missing socket and a dead socket this way; permission and other errors stay unknown.
		if strings.Contains(stderr, "no server running on") || (strings.Contains(stderr, "error connecting to") && strings.Contains(stderr, "No such file or directory")) {
			return "", terminalpkg.ErrNoServer
		}
		return "", fmt.Errorf("tmux list-panes exited %d: %s", code, strings.TrimSpace(stderr))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func list(ctx context.Context, sys terminalpkg.System, server string) ([]terminalpkg.Pane, error) {
	out, err := listPanes(ctx, sys, server, identity)
	if err != nil {
		return nil, err
	}
	panes := []terminalpkg.Pane{}
	index := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, " ")
		if len(fields) != 2 || !paneID.MatchString(fields[0]) {
			return nil, errors.New("malformed tmux pane listing; existence is unknown")
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			return nil, errors.New("malformed tmux pane pid; existence is unknown")
		}
		if _, dup := index[fields[0]]; dup {
			return nil, errors.New("duplicate tmux pane id; existence is unknown")
		}
		index[fields[0]] = len(panes)
		panes = append(panes, terminalpkg.Pane{ID: fields[0], PID: pid})
	}
	// Display text is best effort: a failed or odd listing leaves it empty and never changes identity.
	if out, err := listPanes(ctx, sys, server, display); err == nil {
		for _, line := range strings.Split(out, "\n") {
			fields := strings.SplitN(line, "|", 4)
			if i, ok := index[fields[0]]; ok && len(fields) == 4 {
				panes[i].Command, panes[i].Label, panes[i].Dir = fields[1], fields[2], fields[3]
			}
		}
	}
	return panes, nil
}

func closePane(ctx context.Context, sys terminalpkg.System, server string, p terminalpkg.Pane) error {
	_, stderr, code, err := sys.Run(ctx, nil, "tmux", "-S", server, "kill-pane", "-t", p.ID)
	if err == nil && code != 0 {
		err = fmt.Errorf("tmux kill-pane exited %d: %s", code, strings.TrimSpace(stderr))
	}
	return err
}

func New(sys terminalpkg.System) adapterpkg.Definition {
	return terminalpkg.New(terminalpkg.Driver{Name: "tmux", Noun: "pane", ValidID: paneID.MatchString, ResolveServer: resolveServer, List: list, Close: closePane}, sys)
}
