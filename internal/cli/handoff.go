package cli

import (
	"context"
	"flag"
	"io"
	"time"

	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	policypkg "github.com/Dematom1/dibs/internal/policy"
	remotepkg "github.com/Dematom1/dibs/internal/remote"
)

// handoffPolicy re-selects a handed-off lease's row for its new owner kind; nil keeps the recorded policy.
func (app application) handoffPolicy() func(resource, ownerKind string) (*policypkg.Policy, error) {
	if !leasePoliciesEnabled() {
		return nil
	}
	return func(resource, ownerKind string) (*policypkg.Policy, error) {
		p, _, err := app.leasePolicy(resource, ownerKind, 0, false, 0, 0)
		return p, err
	}
}

// runHandoff serves dibs handoff; its output follows consumer contract 1.
func (app application) runHandoff(ctx context.Context, args []string, out io.Writer, mode outputpkg.Mode) int {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wantHelp := fs.Bool("help", false, "help")
	outputpkg.Flags(fs, &mode)
	owner := fs.String("owner", "", "the new owner")
	stale := fs.Duration("stale", 15*time.Minute, "heartbeat staleness")
	confirm := fs.Bool("confirm", false, "hand off instead of previewing")
	if err := parseFlags(fs, args); err != nil {
		return outputpkg.Error(out, mode, 2, err.Error(), help("handoff"))
	}
	if *wantHelp {
		emitHelp(out, mode, "handoff")
		return 0
	}
	if !handoffEnabled() {
		return outputpkg.Error(out, mode, 2, handoffOff, help("handoff"))
	}
	if fs.NArg() != 1 || !leasepkg.ValidID(fs.Arg(0)) || *owner == "" {
		return outputpkg.Error(out, mode, 2, "use dibs handoff <id> --owner KIND:ID", help("handoff"))
	}
	id := fs.Arg(0)
	reference, err := app.owners.Bind(ctx, *owner, *stale, time.Now().UTC())
	if err != nil {
		return outputpkg.Error(out, mode, 2, err.Error(), help("handoff"))
	}
	request := leasepkg.HandoffRequest{ID: id, Owner: reference, Confirm: *confirm, Policy: app.handoffPolicy()}
	r, err := app.leases.Handoff(ctx, request)
	if err != nil {
		return app.contractError(out, mode, "handoff", id, err)
	}
	result := map[string]any{"id": id, "from": r.From.String(), "owner": reference.String(), "liveness": string(r.Liveness)}
	left := r.From.Kind == leasepkg.ScopeKind && r.Status != "already_done"
	switch r.Status {
	case "already_done":
		result["help"] = []string{"Lease " + id + " already belongs to " + reference.String() + "; nothing changed"}
	case "preview":
		result["help"] = []string{"Run dibs handoff " + id + " --owner " + remotepkg.ShellQuote(*owner) + " --confirm to hand the lease to its new owner"}
	default:
		result["help"] = []string{"Run dibs explain " + id + " to see the new owner and the handoff record"}
	}
	if left {
		result["left_scope"] = r.From.ID
		if r.Status == "handed_off" {
			result["help"] = append(result["help"].([]string), "dibs scope end "+r.From.ID+" no longer releases this lease")
		}
	}
	outputpkg.Emit(out, mode, contractResult(r.Status, result))
	return 0
}
