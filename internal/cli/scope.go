package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Dematom1/dibs/internal/compat"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	remotepkg "github.com/Dematom1/dibs/internal/remote"
)

// runScope serves dibs scope start, end and show; their output follows consumer contract 1.
func (app application) runScope(ctx context.Context, args []string, out io.Writer, mode outputpkg.Mode) int {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("scope", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wantHelp := fs.Bool("help", false, "help")
	full := fs.Bool("full", false, "complete AXI lease records")
	outputpkg.Flags(fs, &mode)
	var key, owner string
	var ttl, stale time.Duration
	var confirm bool
	switch sub {
	case "start":
		fs.StringVar(&key, "key", "", "the caller's id for the work")
		fs.StringVar(&owner, "owner", "", "the scope's owner")
		fs.DurationVar(&ttl, "ttl", 8*time.Hour, "scope lifetime")
		fs.DurationVar(&stale, "stale", 15*time.Minute, "heartbeat staleness")
	case "show":
		fs.StringVar(&key, "key", "", "find the scope by key")
	case "end":
		fs.BoolVar(&confirm, "confirm", false, "end the scope instead of previewing")
	}
	if err := parseFlags(fs, args); err != nil {
		return outputpkg.Error(out, mode, 2, err.Error(), help("scope"))
	}
	if *wantHelp {
		emitHelp(out, mode, "scope")
		return 0
	}
	if !scopesEnabled() {
		return outputpkg.Error(out, mode, 2, scopesOff, help("scope"))
	}
	valid := false
	switch sub {
	case "start":
		valid = fs.NArg() == 0
	case "end":
		valid = fs.NArg() == 1 && leasepkg.ValidID(fs.Arg(0))
	case "show":
		valid = (fs.NArg() == 1 && leasepkg.ValidID(fs.Arg(0)) && key == "") || (fs.NArg() == 0 && key != "")
	}
	if !valid {
		return outputpkg.Error(out, mode, 2, "use dibs scope start, dibs scope end <id>, or dibs scope show <id> | --key K", help("scope"))
	}
	if key != "" && !leasepkg.ValidKey(key) {
		return outputpkg.Error(out, mode, 2, keyRule, help("scope"))
	}
	if sub == "start" && ttl <= 0 {
		return outputpkg.Error(out, mode, 2, "--ttl must be positive", help("scope"))
	}
	id := fs.Arg(0)
	selector := id
	if id == "" {
		selector = "--key " + remotepkg.ShellQuote(key)
	}
	out = &outputpkg.Writer{Writer: out, Mode: mode.Effective(out), Full: *full, FullCommand: "dibs --axi scope show " + selector + " --full"}
	switch sub {
	case "start":
		if owner == "" {
			owner = compat.Env("OWNER")
		}
		if owner == "" {
			owner = "pid:" + strconv.Itoa(os.Getppid())
		}
		reference, err := app.owners.Bind(ctx, owner, stale, time.Now().UTC())
		if err == nil && reference.Kind == leasepkg.ScopeKind {
			err = errors.New("a scope cannot be owned by another scope")
		}
		if err != nil {
			return outputpkg.Error(out, mode, 2, err.Error(), help("scope"))
		}
		l, done, err := app.leases.StartScope(ctx, leasepkg.ScopeRequest{Owner: reference, TTL: ttl, Key: key})
		if err != nil {
			return app.contractError(out, mode, "scope start", "", err)
		}
		status := "created"
		if done {
			status = "already_done"
		}
		return emitScope(out, mode, status, map[string]any{"id": l.ID, "scope": outputpkg.LeaseView(l, ownerpkg.Alive, time.Now().UTC(), ""), "help": []string{
			"Lease resources for this work with --owner scope:" + l.ID + " on dibs create or dibs adopt",
			"Run dibs extend " + l.ID + " --ttl " + ttl.String() + " to renew the scope",
			"Run dibs scope end " + l.ID + " --confirm when the work is done",
		}})
	case "show":
		v, err := app.leases.ShowScope(ctx, id, key)
		var refusal *leasepkg.Refusal
		if errors.As(err, &refusal) && refusal.Reason == "not_found" {
			return emitScope(out, mode, "", refusalFields(map[string]any{}, refusal, id), "Run dibs scope start to create a scope")
		}
		if err != nil {
			return app.contractError(out, mode, "scope show", id, err)
		}
		scope := v.Scope.Lease
		views, records := []map[string]any{}, []map[string]any{}
		for _, member := range v.Members {
			views = append(views, outputpkg.LeaseView(member.Lease, member.Liveness, v.At, kubeconfigFor(app.resources, member.Lease)))
			records = append(records, outputpkg.LeaseRecord(member.Lease, kubeconfigFor(app.resources, member.Lease)))
		}
		hints := []string{"Run dibs scope end " + scope.ID + " to preview ending the scope", "Run dibs explain <id> to see why a member is held"}
		return emitScope(out, mode, "shown", map[string]any{"id": scope.ID, "scope": outputpkg.LeaseView(scope, v.Scope.Liveness, v.At, ""), "record": outputpkg.LeaseRecord(scope, ""), "count": len(views), "leases": views, "records": records, "help": hints})
	}
	r, err := app.leases.EndScope(ctx, id, confirm)
	if err != nil {
		return app.contractOperationError(out, mode, "scope end", id, err)
	}
	result := map[string]any{"id": id}
	switch {
	case r.Status == "already_done":
		result["help"] = []string{"No scope " + id + " remains on this machine; nothing to end"}
	case r.Status == "preview":
		result["release"] = r.Members
		result["help"] = []string{"Run dibs scope end " + id + " --confirm to expire the scope and release the " + strconv.Itoa(len(r.Members)) + " leases it owns, newest first"}
	default:
		result["released"], result["retained"], result["forgotten"] = r.Released, r.Members, r.Forgotten
		result["help"] = []string{"The scope has ended and every lease it owned is released"}
		if len(r.Members) > 0 || len(r.Failures) > 0 {
			result["failures"], result["error"] = r.Failures, "some leases could not be released; they and the scope are retained"
			result["code"], result["exit_code"] = "COMMAND_FAILED", 1
			result["help"] = []string{"Resolve the failures, then rerun dibs scope end " + id + " --confirm; it releases only what remains"}
			return emitScope(out, mode, r.Status, result)
		}
	}
	return emitScope(out, mode, r.Status, result)
}

// emitScope prints a contract result; extra hints replace a refusal's generic one.
func emitScope(out io.Writer, mode outputpkg.Mode, status string, result map[string]any, hints ...string) int {
	if status != "" {
		result = contractResult(status, result)
	}
	if len(hints) > 0 {
		result["help"] = hints
	}
	outputpkg.Emit(out, mode, result)
	if code, ok := result["exit_code"].(int); ok {
		return code
	}
	return 0
}
