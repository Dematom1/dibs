package adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type CommandRunner func(context.Context, []string, string, ...string) ([]byte, error)

// DirRunner runs a tool in a pinned working directory, for tools without a -C flag.
type DirRunner func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)

// CommandError reports a failed tool call; Code is the exit status, or -1 when the tool did not exit normally.
type CommandError struct{ Code int }

func (CommandError) Error() string {
	return "resource operation failed; check installed resource tools and their connectivity, then retry dibs release or dibs sweep --confirm"
}

func RunCommand(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	return RunCommandIn(ctx, "", env, name, args...)
}

func RunCommandIn(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(callerEnv(), env...)
	cmd.Stdin = nil
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprint(os.Stderr, diagnostics.String())
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		return nil, CommandError{code}
	}
	return out, nil
}

func DockerEnv(ref ResourceRef) []string {
	return []string{"DOCKER_HOST=", "DOCKER_CONTEXT=" + ref.Context, "KIND_EXPERIMENTAL_PROVIDER=docker", "COMPOSE_INTERACTIVE_NO_CLI=1"}
}

// gitLocation names variables that redirect git away from the directory a
// command targets with -C; a caller's values must never steer an adapter.
var gitLocation = map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_COMMON_DIR": true, "GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true, "GIT_PREFIX": true}

func callerEnv() []string {
	var kept []string
	for _, entry := range os.Environ() {
		if name, _, _ := strings.Cut(entry, "="); !gitLocation[name] {
			kept = append(kept, entry)
		}
	}
	return kept
}
