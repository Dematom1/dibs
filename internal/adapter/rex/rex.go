package rex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	terminalpkg "github.com/Dematom1/dibs/internal/adapter/terminal"
)

var blockID = regexp.MustCompile(`^block:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// binary prefers rex on PATH, then the copy Rex exposes to its own terminals.
func binary() string {
	if path, err := exec.LookPath("rex"); err == nil {
		return path
	}
	if dir := os.Getenv("REX_BIN_DIR"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "rex")
	}
	return "rex"
}

// An inherited REX_SESSION scopes block lookups to the caller's session, so a block elsewhere would read as absent.
var serverWide = []string{"REX_SESSION=", "REX_BLOCK="}

// rex autostarts a missing local server by default; dibs must observe, never start one.
func rex(ctx context.Context, sys terminalpkg.System, server string, args ...string) ([]byte, string, int, error) {
	base := []string{"--autostart=false"}
	if server != "" {
		base = append(base, "-S", server)
	}
	return sys.Run(ctx, serverWide, binary(), append(base, args...)...)
}

func decode(ctx context.Context, sys terminalpkg.System, server string, into any, args ...string) error {
	out, stderr, code, err := rex(ctx, sys, server, args...)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("rex %s exited %d: %s", strings.Join(args, " "), code, strings.TrimSpace(stderr))
	}
	if json.Unmarshal(out, into) != nil {
		return fmt.Errorf("malformed rex %s output; existence is unknown", strings.Join(args, " "))
	}
	return nil
}

// resolveServer pins the endpoint the server reports, so later calls reach the same server.
func resolveServer(ctx context.Context, sys terminalpkg.System, requested string) (string, error) {
	var who struct {
		Endpoint string `json:"endpoint"`
	}
	if err := decode(ctx, sys, requested, &who, "whoami", "--json"); err != nil {
		return "", err
	}
	if who.Endpoint == "" {
		return "", errors.New("rex whoami reported no server endpoint")
	}
	return who.Endpoint, nil
}

type process struct {
	Child *struct {
		PID int `json:"pid"`
	} `json:"child"`
	Foreground *struct {
		Name  string `json:"name"`
		Argv0 string `json:"argv0"`
		Cwd   string `json:"cwd"`
	} `json:"foreground"`
}

func lookup(ctx context.Context, sys terminalpkg.System, server, id string) (terminalpkg.Pane, bool, error) {
	out, stderr, code, err := rex(ctx, sys, server, "block", "call", "process", "-b", id)
	if err != nil {
		return terminalpkg.Pane{}, false, err
	}
	// Exit 3 with "not found" comes from a reachable server; an unreachable server exits 4 and stays unknown.
	if code == 3 && strings.Contains(stderr, "not found") {
		return terminalpkg.Pane{}, false, nil
	}
	if code != 0 {
		return terminalpkg.Pane{}, false, fmt.Errorf("rex block call process exited %d: %s", code, strings.TrimSpace(stderr))
	}
	var p process
	if json.Unmarshal(out, &p) != nil {
		return terminalpkg.Pane{}, false, errors.New("malformed rex block process output; existence is unknown")
	}
	pane := terminalpkg.Pane{ID: id}
	if p.Child != nil {
		pane.PID = p.Child.PID
	}
	if p.Foreground != nil {
		// A versioned executable such as claude reports its version as name; argv0 is what a person typed.
		pane.Command, pane.Dir = p.Foreground.Name, p.Foreground.Cwd
		if p.Foreground.Argv0 != "" {
			pane.Command = filepath.Base(p.Foreground.Argv0)
		}
	}
	return pane, true, nil
}

func list(ctx context.Context, sys terminalpkg.System, server string) ([]terminalpkg.Pane, error) {
	var sessions struct {
		Sessions *[]struct {
			ID string `json:"session_id"`
		} `json:"sessions"`
	}
	if err := decode(ctx, sys, server, &sessions, "ls", "--json"); err != nil {
		return nil, err
	}
	if sessions.Sessions == nil {
		return nil, errors.New("rex session listing has no sessions field; existence is unknown")
	}
	panes := []terminalpkg.Pane{}
	for _, session := range *sessions.Sessions {
		var blocks struct {
			Blocks *[]struct {
				ID     string `json:"block_id"`
				Label  string `json:"label"`
				Flavor string `json:"flavor"`
			} `json:"blocks"`
		}
		if err := decode(ctx, sys, server, &blocks, "block", "ls", "-s", session.ID, "--json"); err != nil {
			return nil, err
		}
		if blocks.Blocks == nil {
			return nil, errors.New("rex block listing has no blocks field; existence is unknown")
		}
		for _, b := range *blocks.Blocks {
			if !strings.HasPrefix(b.Flavor, "com.superlogical.terminal") {
				continue
			}
			if !blockID.MatchString(b.ID) {
				return nil, errors.New("malformed rex block id; existence is unknown")
			}
			p, found, err := lookup(ctx, sys, server, b.ID)
			if err != nil {
				return nil, err
			}
			if found {
				p.Label = b.Label
				panes = append(panes, p)
			}
		}
	}
	return panes, nil
}

func closeBlock(ctx context.Context, sys terminalpkg.System, server string, p terminalpkg.Pane) error {
	_, stderr, code, err := rex(ctx, sys, server, "block", "close", p.ID)
	if err == nil && code != 0 {
		err = fmt.Errorf("rex block close exited %d: %s", code, strings.TrimSpace(stderr))
	}
	return err
}

func New(sys terminalpkg.System) adapterpkg.Definition {
	return terminalpkg.New(terminalpkg.Driver{Name: "rex", Noun: "block", ValidID: blockID.MatchString, ResolveServer: resolveServer, List: list, Lookup: lookup, Close: closeBlock}, sys)
}
