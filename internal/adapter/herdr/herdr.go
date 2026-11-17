package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	terminalpkg "github.com/Dematom1/dibs/internal/adapter/terminal"
)

var (
	paneID      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
	sessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
)

// The pinned --session decides the server; an inherited pane context must never retarget a call.
var isolated = []string{"HERDR_SOCKET_PATH=", "HERDR_SESSION=", "HERDR_PANE_ID=", "HERDR_TAB_ID=", "HERDR_WORKSPACE_ID="}

func resolveServer(_ context.Context, _ terminalpkg.System, requested string) (string, error) {
	session := requested
	if session == "" {
		session = os.Getenv("HERDR_SESSION")
	}
	if session == "" {
		session = "default"
	}
	if !sessionName.MatchString(session) {
		return "", errors.New("herdr server must be a session name of letters, digits, underscores, or dashes")
	}
	return session, nil
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func call(ctx context.Context, sys terminalpkg.System, session string, result any, args ...string) error {
	out, stderr, code, err := sys.Run(ctx, isolated, "herdr", append(args, "--session", session)...)
	if err != nil {
		return err
	}
	var r response
	for _, text := range []string{string(out), stderr} {
		if json.Unmarshal([]byte(strings.TrimSpace(text)), &r) == nil && (r.Error != nil || r.Result != nil) {
			break
		}
	}
	if r.Error != nil {
		if r.Error.Code == "server_not_running" {
			return terminalpkg.ErrNoServer
		}
		return fmt.Errorf("herdr %s: %s", r.Error.Code, r.Error.Message)
	}
	if code != 0 || r.Result == nil || json.Unmarshal(r.Result, result) != nil {
		return fmt.Errorf("herdr %s exited %d without a readable result; existence is unknown", strings.Join(args, " "), code)
	}
	return nil
}

type paneInfo struct {
	PaneID string  `json:"pane_id"`
	Label  *string `json:"label"`
	Title  *string `json:"title"`
	Cwd    *string `json:"cwd"`
	Agent  *string `json:"agent"`
}

func shellPID(ctx context.Context, sys terminalpkg.System, session, id string) (int, error) {
	var r struct {
		ProcessInfo struct {
			PaneID   string `json:"pane_id"`
			ShellPID *int   `json:"shell_pid"`
		} `json:"process_info"`
	}
	if err := call(ctx, sys, session, &r, "pane", "process-info", "--pane", id); err != nil {
		return 0, err
	}
	if r.ProcessInfo.PaneID != id {
		return 0, errors.New("herdr process info names another pane; existence is unknown")
	}
	if r.ProcessInfo.ShellPID == nil {
		return 0, nil
	}
	return *r.ProcessInfo.ShellPID, nil
}

func text(values ...*string) string {
	for _, v := range values {
		if v != nil && *v != "" {
			return *v
		}
	}
	return ""
}

func list(ctx context.Context, sys terminalpkg.System, session string) ([]terminalpkg.Pane, error) {
	var r struct {
		Panes *[]paneInfo `json:"panes"`
	}
	if err := call(ctx, sys, session, &r, "pane", "list"); err != nil {
		return nil, err
	}
	if r.Panes == nil {
		return nil, errors.New("herdr pane list has no panes field; existence is unknown")
	}
	panes := []terminalpkg.Pane{}
	for _, info := range *r.Panes {
		if !paneID.MatchString(info.PaneID) {
			return nil, errors.New("malformed herdr pane id; existence is unknown")
		}
		pid, err := shellPID(ctx, sys, session, info.PaneID)
		if err != nil {
			return nil, err
		}
		panes = append(panes, terminalpkg.Pane{ID: info.PaneID, PID: pid, Label: text(info.Label, info.Title), Command: text(info.Agent), Dir: text(info.Cwd)})
	}
	return panes, nil
}

func closePane(ctx context.Context, sys terminalpkg.System, session string, p terminalpkg.Pane) error {
	var ignored any
	return call(ctx, sys, session, &ignored, "pane", "close", p.ID)
}

func New(sys terminalpkg.System) adapterpkg.Definition {
	return terminalpkg.New(terminalpkg.Driver{Name: "herdr", Noun: "pane", ValidID: paneID.MatchString, ResolveServer: resolveServer, List: list, Close: closePane}, sys)
}
