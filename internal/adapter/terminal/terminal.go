// Package terminal reclaims adopted terminals (panes, blocks, subshells) only
// when their pinned process proves which terminal is ours.
package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

// Pane is one terminal as its tool reports it. PID is the terminal's direct
// child process, or 0 when the tool reports none.
type Pane struct {
	ID       string
	PID      int
	Label    string
	Command  string
	Dir      string
	ChildPID int
}

// ErrNoServer is proof that the server holds no terminals. Drivers return it
// only when the tool says its server is not running; every other failure is unknown.
var ErrNoServer = errors.New("terminal server is not running")

// Driver is what one terminal tool supplies to the shared lifecycle.
type Driver struct {
	Name          string
	Noun          string
	ValidID       func(string) bool
	ResolveServer func(ctx context.Context, sys System, requested string) (string, error)
	List          func(ctx context.Context, sys System, server string) ([]Pane, error)
	// Lookup reports one terminal; found=false is proof of absence. Nil uses List.
	Lookup func(ctx context.Context, sys System, server, id string) (Pane, bool, error)
	// Hangup signals the terminal's shell. Nil sends SIGHUP to the pinned process.
	Hangup func(ctx context.Context, sys System, p Pane)
	Close  func(ctx context.Context, sys System, server string, p Pane) error
}

// System holds every side effect so each mapping branch is testable without the real tool.
type System struct {
	// Run reports the exit code of a command that ran; err is set only when it could not run or timed out.
	// An env entry "NAME=" removes NAME from the inherited environment.
	Run     func(ctx context.Context, env []string, name string, args ...string) (out []byte, stderr string, code int, err error)
	Process func(pid int) (string, ownerpkg.Liveness)
	Signal  func(pid int, sig syscall.Signal) error
	Sleep   func(time.Duration)
	Grace   time.Duration
}

func Real() System {
	return System{Run: run, Process: ownerpkg.AnyProcessIdentity, Signal: syscall.Kill, Sleep: time.Sleep, Grace: 3 * time.Second}
}

func run(ctx context.Context, env []string, name string, args ...string) ([]byte, string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = mergeEnv(os.Environ(), env)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		return out, stderr.String(), exit.ExitCode(), nil
	}
	if err != nil {
		return nil, stderr.String(), -1, fmt.Errorf("%s did not complete: %w", name, err)
	}
	return out, stderr.String(), 0, nil
}

func mergeEnv(base, overrides []string) []string {
	drop := map[string]bool{}
	for _, entry := range overrides {
		key, _, _ := strings.Cut(entry, "=")
		drop[key] = true
	}
	merged := []string{}
	for _, entry := range base {
		if key, _, _ := strings.Cut(entry, "="); !drop[key] {
			merged = append(merged, entry)
		}
	}
	for _, entry := range overrides {
		if _, value, _ := strings.Cut(entry, "="); value != "" {
			merged = append(merged, entry)
		}
	}
	return merged
}

type resource struct {
	driver Driver
	sys    System
}

func New(d Driver, sys System) adapterpkg.Definition {
	r := resource{d, sys}
	return adapterpkg.Definition{Name: d.Name, Adapter: r, Discover: r.discover, Prepare: r.prepare, Validate: r.validate,
		Env: func(adapterpkg.ResourceRef) adapterpkg.Environment { return adapterpkg.Environment{} },
		Terminal: &adapterpkg.TerminalSupport{
			ResolveServer: func(ctx context.Context, requested string) (string, error) {
				return d.ResolveServer(ctx, sys, requested)
			},
			ValidID:  d.ValidID,
			Describe: r.describe,
		},
	}
}

func (r resource) Create(context.Context, adapterpkg.Target) (adapterpkg.ResourceRef, error) {
	return adapterpkg.ResourceRef{}, errors.New("terminals enter dibs only through dibs adopt")
}

func (r resource) lookup(ctx context.Context, server, id string) (Pane, bool, error) {
	if r.driver.Lookup != nil {
		return r.driver.Lookup(ctx, r.sys, server, id)
	}
	panes, err := r.driver.List(ctx, r.sys, server)
	if errors.Is(err, ErrNoServer) {
		return Pane{}, false, nil
	}
	if err != nil {
		return Pane{}, false, err
	}
	for _, p := range panes {
		if p.ID == id {
			return p, true, nil
		}
	}
	return Pane{}, false, nil
}

func (r resource) discover(ctx context.Context, ref adapterpkg.ResourceRef) ([]map[string]any, error) {
	server, err := r.server(ctx, ref)
	if err != nil {
		return nil, err
	}
	panes, err := r.driver.List(ctx, r.sys, server)
	if errors.Is(err, ErrNoServer) {
		return nil, fmt.Errorf("no %s server is running on %s", r.driver.Name, server)
	}
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, p := range panes {
		start := ""
		if p.PID > 0 {
			start, _ = r.sys.Process(p.PID)
		}
		rows = append(rows, map[string]any{"type": r.driver.Name, "name": p.ID, "server": server, "pid": p.PID, "start": start, "command": p.Command, "label": p.Label, "dir": p.Dir})
	}
	return rows, nil
}

func (r resource) server(ctx context.Context, ref adapterpkg.ResourceRef) (string, error) {
	if ref.Terminal != nil && ref.Terminal.Server != "" {
		return ref.Terminal.Server, nil
	}
	return r.driver.ResolveServer(ctx, r.sys, "")
}

// prepare pins the terminal's direct child process with its start time, the identity every later reclaim must match.
func (r resource) prepare(ctx context.Context, request adapterpkg.PrepareRequest) (adapterpkg.ResourceRef, error) {
	existing := request.Existing
	if !existing.Adopted || !r.driver.ValidID(existing.Name) {
		return adapterpkg.ResourceRef{}, errors.New("terminals enter dibs only through dibs adopt")
	}
	server, err := r.server(ctx, existing)
	if err != nil {
		return adapterpkg.ResourceRef{}, err
	}
	p, found, err := r.lookup(ctx, server, existing.Name)
	if err != nil {
		return adapterpkg.ResourceRef{}, err
	}
	if !found || p.PID <= 0 {
		return adapterpkg.ResourceRef{}, fmt.Errorf("%s %s has no running process; adoption refused", r.driver.Noun, existing.Name)
	}
	start, live := r.sys.Process(p.PID)
	if live != ownerpkg.Alive || start == "" {
		return adapterpkg.ResourceRef{}, fmt.Errorf("%s %s process %d is not alive and inspectable; adoption refused", r.driver.Noun, existing.Name, p.PID)
	}
	return adapterpkg.ResourceRef{Adopted: true, Adapter: r.driver.Name, Name: existing.Name, Terminal: &adapterpkg.TerminalPin{Server: server, PID: p.PID, Start: start}}, nil
}

func (r resource) validate(_ string, _ adapterpkg.Metadata, ref adapterpkg.ResourceRef) error {
	pin := ref.Terminal
	if !ref.Adopted || !r.driver.ValidID(ref.Name) || pin == nil || pin.Server == "" || pin.PID <= 0 || pin.Start == "" || ref.Context != "" || ref.DockerHost != "" || ref.File != "" {
		return errors.New("invalid adopted terminal identity")
	}
	return nil
}

func (r resource) describe(ref adapterpkg.ResourceRef) string {
	pin := ref.Terminal
	if pin == nil {
		return r.kind() + " " + ref.Name
	}
	return fmt.Sprintf("%s %s on %s (pid %d started %s)", r.kind(), ref.Name, pin.Server, pin.PID, pin.Start)
}

// kind names the terminal type for people: "tmux pane", or "treehouse-subshell" when the name already says it.
func (r resource) kind() string {
	if strings.HasSuffix(r.driver.Name, "-"+r.driver.Noun) {
		return r.driver.Name
	}
	return r.driver.Name + " " + r.driver.Noun
}

type state int

const (
	absent   state = iota // our process is gone and no terminal carries it; a reused ID is left alone
	owned                 // the terminal still carries our live process
	leftover              // our process is dead but its terminal still shows it, e.g. remain-on-exit
)

// observe maps what the tool and the process table report to one decision. Any
// doubt is an error, so the lease is retained and nothing is touched.
func (r resource) observe(ctx context.Context, ref adapterpkg.ResourceRef) (state, Pane, error) {
	pin := ref.Terminal
	if pin == nil {
		return 0, Pane{}, errors.New("terminal lease has no pinned identity; lease retained")
	}
	start, live := r.sys.Process(pin.PID)
	if live == ownerpkg.Unknown {
		return 0, Pane{}, fmt.Errorf("cannot inspect pinned process %d; lease retained", pin.PID)
	}
	p, found, err := r.lookup(ctx, pin.Server, ref.Name)
	if err != nil {
		return 0, Pane{}, fmt.Errorf("cannot list %s terminals on %s; lease retained: %w", r.driver.Name, pin.Server, err)
	}
	carries := found && p.PID == pin.PID
	switch {
	case live == ownerpkg.Alive && start == pin.Start && carries:
		return owned, p, nil
	case live == ownerpkg.Alive && start == pin.Start:
		return 0, Pane{}, fmt.Errorf("pinned process %d is alive but %s %s no longer carries it; touching nothing, lease retained", pin.PID, r.driver.Noun, ref.Name)
	case live == ownerpkg.Alive && carries:
		return 0, Pane{}, fmt.Errorf("%s %s carries reused pid %d; touching nothing, lease retained", r.driver.Noun, ref.Name, pin.PID)
	case live == ownerpkg.Dead && carries:
		return leftover, p, nil
	}
	return absent, Pane{}, nil
}

// Destroy hangs up the shell, closes the terminal, and returns nil only after
// both the terminal and its pinned process are verified gone.
func (r resource) Destroy(ctx context.Context, t adapterpkg.Target) error {
	s, p, err := r.observe(ctx, t.Ref)
	if err != nil || s == absent {
		return err
	}
	childPID, childStart := 0, ""
	if s == owned && p.ChildPID > 0 {
		var live ownerpkg.Liveness
		childStart, live = r.sys.Process(p.ChildPID)
		if live == ownerpkg.Unknown {
			return fmt.Errorf("cannot inspect held shell %d; lease retained", p.ChildPID)
		}
		if live == ownerpkg.Alive {
			childPID = p.ChildPID
		}
	}
	if s == owned {
		if r.driver.Hangup != nil {
			r.driver.Hangup(ctx, r.sys, p)
		} else {
			// EPERM (a root login) leaves the hangup to the pty close below.
			_ = r.sys.Signal(p.PID, syscall.SIGHUP)
		}
		if s, p, err = r.settle(ctx, t.Ref); err != nil {
			return err
		}
		if s == absent {
			return r.settleChild(ctx, childPID, childStart)
		}
	}
	if err := r.driver.Close(ctx, r.sys, t.Ref.Terminal.Server, p); err != nil {
		if s, _, observeErr := r.observe(ctx, t.Ref); observeErr == nil && s == absent {
			return r.settleChild(ctx, childPID, childStart)
		}
		return fmt.Errorf("cannot close %s %s; lease retained: %w", r.driver.Noun, t.Ref.Name, err)
	}
	if s, _, err = r.settle(ctx, t.Ref); err != nil {
		return err
	}
	if s == absent {
		return r.settleChild(ctx, childPID, childStart)
	}
	return fmt.Errorf("%s %s survived cleanup; retry dibs release", r.driver.Noun, t.Ref.Name)
}

// settle polls until the terminal is absent or the grace period ends.
func (r resource) settleChild(ctx context.Context, pid int, start string) error {
	if pid == 0 {
		return nil
	}
	const step = 100 * time.Millisecond
	for waited := time.Duration(0); ; waited += step {
		current, live := r.sys.Process(pid)
		switch live {
		case ownerpkg.Dead:
			return nil
		case ownerpkg.Unknown:
			return fmt.Errorf("cannot verify held shell %d is gone; lease retained", pid)
		case ownerpkg.Alive:
			if current != start {
				return nil
			}
		}
		if waited >= r.sys.Grace {
			return fmt.Errorf("held shell %d survived cleanup; lease retained", pid)
		}
		r.sys.Sleep(step)
	}
}

func (r resource) settle(ctx context.Context, ref adapterpkg.ResourceRef) (state, Pane, error) {
	const step = 100 * time.Millisecond
	for waited := time.Duration(0); ; waited += step {
		s, p, err := r.observe(ctx, ref)
		if err != nil || s == absent || waited >= r.sys.Grace {
			return s, p, err
		}
		r.sys.Sleep(step)
	}
}
