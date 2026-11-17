package cli

import (
	"context"
	"flag"
	adapterpkg "github.com/Dematom1/dibs/internal/adapter"
	leasepkg "github.com/Dematom1/dibs/internal/lease"
	outputpkg "github.com/Dematom1/dibs/internal/output"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
	policypkg "github.com/Dematom1/dibs/internal/policy"
	remotepkg "github.com/Dematom1/dibs/internal/remote"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	treehousepkg "github.com/Dematom1/dibs/internal/adapter/treehouseworktree"
)

func adoptionCommand(row map[string]any) string {
	if server, ok := row["server"].(string); ok {
		return "dibs adopt " + row["type"].(string) + " " + remotepkg.ShellQuote(row["name"].(string)) + " --server " + remotepkg.ShellQuote(server) + " --owner '<owner>' --ttl '<duration>'"
	}
	command := "dibs adopt " + row["type"].(string) + " " + remotepkg.ShellQuote(row["name"].(string)) + " --context " + remotepkg.ShellQuote(row["docker_context"].(string))
	if row["type"] == "compose" {
		// Multiple original files need an explicitly resolved single config, not a guessed split on commas.
		file, _ := row["config_files"].(string)
		if file == "" || strings.Contains(file, ",") {
			file = "<resolved-compose-file>"
		}
		command += " --file " + remotepkg.ShellQuote(file)
	}
	return command + " --owner '<owner>' --ttl '<duration>'"
}

type verifiedAdoption struct {
	owner, claim, key, pending, context, file string
	protect                                   bool
	stale, ttl, idle, maxLifetime             time.Duration
	excluded                                  []string
}

// adoptVerified registers a resource whose adapter verifies adoption itself;
// it needs no Docker context and has no discovery, so --pending does not apply.
func (app application) adoptVerified(ctx context.Context, d adapterpkg.Definition, fs *flag.FlagSet, out io.Writer, mode outputpkg.Mode, a verifiedAdoption) int {
	if !d.VerifiesAdoption {
		return outputpkg.Error(out, mode, 2, "the "+d.Name+" adapter is off; enable it with "+d.OptIn, help("adopt"))
	}
	path := fs.Arg(1)
	// With lease policies on, the policy row supplies the TTL.
	if fs.NArg() != 2 || !filepath.IsAbs(path) || filepath.Clean(path) != path || a.claim == "" || a.owner == "" || a.ttl < 0 || (a.ttl == 0 && !leasePoliciesEnabled()) || a.context != "" || a.file != "" || a.pending != "" {
		return outputpkg.Error(out, mode, 2, "require "+d.Name+", an absolute clean slot path, --lease-id, explicit --owner and positive --ttl; --context, --file and --pending do not apply", help("adopt"))
	}
	for _, remote := range a.excluded {
		if !treehousepkg.ValidRemote(remote) {
			return outputpkg.Error(out, mode, 2, "--landed-exclude-remote needs a plain git remote name", help("adopt"))
		}
	}
	if a.key != "" && !consumerContractEnabled() {
		return outputpkg.Error(out, mode, 2, consumerContractOff, help("adopt"))
	}
	if a.protect && !liveOwnerProtectionEnabled() {
		return outputpkg.Error(out, mode, 2, liveOwnerProtectionOff, help("adopt"))
	}
	if a.key != "" && !leasepkg.ValidKey(a.key) {
		return outputpkg.Error(out, mode, 2, keyRule, help("adopt"))
	}
	owner, err := app.owners.Bind(ctx, a.owner, a.stale, time.Now().UTC())
	if err != nil {
		return outputpkg.Error(out, mode, 2, err.Error(), help("adopt"))
	}
	policy, ttl, err := app.leasePolicy(d.Name, owner.Kind, a.ttl, a.ttl > 0, a.idle, a.maxLifetime)
	if err != nil {
		return outputpkg.Error(out, mode, 2, err.Error(), help("adopt"))
	}
	return app.adopt(ctx, out, mode, leasepkg.AdoptRequest{Owner: owner, TTL: ttl, Resource: adapterpkg.ResourceRef{Adapter: d.Name, Path: path, Claim: a.claim}, ExcludeRemotes: a.excluded, Key: a.key, ProtectLiveOwner: a.protect, Policy: policy}, a.key != "")
}

func (app application) runInventory(ctx context.Context, command string, args []string, out io.Writer, mode outputpkg.Mode) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wantHelp := fs.Bool("help", false, "help")
	full := fs.Bool("full", false, "complete AXI lease record")
	outputpkg.Flags(fs, &mode)
	contextName := fs.String("context", "", "Docker context (default selected context)")
	server := fs.String("server", "", "terminal server: tmux socket, herdr session, or rex endpoint")
	var owner, file, key, pending, claim, kind string
	var protect bool
	var ttl, idle, maxLifetime time.Duration
	var excluded listFlag
	stale := 15 * time.Minute
	if command == "adopt" {
		fs.StringVar(&owner, "owner", "", "required named owner")
		fs.DurationVar(&ttl, "ttl", 0, "required positive lifetime")
		fs.DurationVar(&stale, "stale", stale, "heartbeat staleness")
		fs.StringVar(&file, "file", "", "Compose file")
		fs.StringVar(&key, "key", "", "idempotency key")
		fs.StringVar(&pending, "pending", "", "bind to this pending lease")
		fs.BoolVar(&protect, "protect-live-owner", false, "reclaim only on proven owner death")
		fs.DurationVar(&idle, "idle", 0, "tighter policy idle timeout")
		fs.DurationVar(&maxLifetime, "max-lifetime", 0, "tighter policy maximum lifetime")
		fs.StringVar(&claim, "lease-id", "", "treehouse lease_id of a durably leased slot")
		fs.Var(&excluded, "landed-exclude-remote", "remote whose refs do not count as landed (repeatable)")
	} else {
		fs.StringVar(&kind, "type", "", "limit to one resource type")
	}
	if err := parseFlags(fs, args); err != nil {
		return outputpkg.Error(out, mode, 2, err.Error(), help(command))
	}
	if *wantHelp {
		emitHelp(out, mode, command)
		return 0
	}
	out = &outputpkg.Writer{Writer: out, Mode: mode.Effective(out), Full: *full, FullCommand: "dibs --axi ps --full"}
	var parsedOwner ownerpkg.Reference
	selected := fs.Arg(0)
	if command == "orphans" {
		selected = kind
	}
	d, known := app.resources.Lookup(selected)
	terminal := known && d.Terminal != nil
	if command == "adopt" && *server != "" && !terminal {
		return outputpkg.Error(out, mode, 2, "--server applies only to the terminal types tmux, herdr, rex, and treehouse-subshell", help(command))
	}
	var leasePolicy *policypkg.Policy
	if command == "adopt" && known && d.Name == treehousepkg.Name {
		return app.adoptVerified(ctx, d, fs, out, mode, verifiedAdoption{owner: owner, claim: claim, key: key, pending: pending, context: *contextName, file: file, protect: protect, stale: stale, ttl: ttl, idle: idle, maxLifetime: maxLifetime, excluded: excluded})
	}
	if command == "adopt" && (claim != "" || len(excluded) > 0) {
		return outputpkg.Error(out, mode, 2, "--lease-id and --landed-exclude-remote apply only to "+treehousepkg.Name, help(command))
	}
	if command == "adopt" {
		if (pending != "" || key != "") && !consumerContractEnabled() {
			return outputpkg.Error(out, mode, 2, consumerContractOff, help(command))
		}
		if pending != "" {
			if fs.NArg() != 2 || !known || d.Discover == nil || terminal || !adapterpkg.ValidResourceName(fs.Arg(1)) || !leasepkg.ValidID(pending) || owner != "" || ttl != 0 || key != "" || protect || *contextName != "" || (!d.RequiresFile && file != "") {
				return outputpkg.Error(out, mode, 2, "--pending takes type, handle and a lease id; the pending lease already fixes owner, TTL, key and context", help(command))
			}
			return app.adopt(ctx, out, mode, leasepkg.AdoptRequest{Resource: adapterpkg.ResourceRef{Adapter: fs.Arg(0), Name: fs.Arg(1)}, File: file, Pending: pending}, true)
		}
		if fs.Arg(0) == "shell" && !shellAdapterEnabled() {
			return outputpkg.Error(out, mode, 2, shellAdapterOff, help(command))
		}
		// With lease policies on, the policy row supplies the TTL.
		if fs.NArg() != 2 || !known || d.Discover == nil || !d.ValidName(fs.Arg(1)) || owner == "" || ttl < 0 || (ttl == 0 && !leasePoliciesEnabled()) || d.RequiresFile != (file != "") || ((d.HostScoped || terminal) && *contextName != "") {
			return outputpkg.Error(out, mode, 2, "require type, name, explicit --owner and positive --ttl; --file is required only for compose, --context only for Docker resources", help(command))
		}
		if protect && !liveOwnerProtectionEnabled() {
			return outputpkg.Error(out, mode, 2, liveOwnerProtectionOff, help(command))
		}
		if key != "" && !leasepkg.ValidKey(key) {
			return outputpkg.Error(out, mode, 2, keyRule, help(command))
		}
	} else if fs.NArg() != 0 || (kind != "" && (!known || d.Discover == nil)) || (*server != "" && !terminal) || (terminal && *contextName != "") {
		return outputpkg.Error(out, mode, 2, "orphans takes no positional arguments; --type names a resource type; --server requires --type with a terminal type", help(command))
	}
	var terminals []string
	if terminal || (command == "orphans" && kind == "") {
		enabled, err := enabledTerminals(app.resources)
		if err != nil {
			return outputpkg.Error(out, mode, 2, err.Error(), help(command))
		}
		terminals = enabled
		if terminal {
			if !slices.Contains(enabled, selected) {
				return outputpkg.Error(out, mode, 2, "terminal adapter "+selected+" is off; enable it with "+terminalOptIn, help(command))
			}
			terminals = []string{selected}
		}
	}
	if command == "adopt" {
		var err error
		parsedOwner, err = app.owners.Bind(ctx, owner, stale, time.Now().UTC())
		if err != nil {
			return outputpkg.Error(out, mode, 2, err.Error(), help(command))
		}
		if leasePolicy, ttl, err = app.leasePolicy(fs.Arg(0), parsedOwner.Kind, ttl, ttl > 0, idle, maxLifetime); err != nil {
			return outputpkg.Error(out, mode, 2, err.Error(), help(command))
		}
		if d.HostScoped {
			return app.adopt(ctx, out, mode, leasepkg.AdoptRequest{Owner: parsedOwner, TTL: ttl, Resource: adapterpkg.ResourceRef{Adapter: fs.Arg(0), Name: fs.Arg(1)}, Key: key, ProtectLiveOwner: protect, Policy: leasePolicy}, key != "")
		}
	}
	var r adapterpkg.ResourceRef
	var dockerErr error
	if !terminal {
		var err error
		r, err = adapterpkg.ResolveDockerContext(ctx, *contextName, adapterpkg.RunCommand)
		// With terminals enabled, plain orphans reports Docker as unavailable instead of hiding the terminal inventory.
		if err != nil && command == "orphans" && kind == "" && len(terminals) > 0 {
			dockerErr = err
		} else if err != nil {
			return outputpkg.Error(out, mode, 1, err.Error(), "Select a reachable Docker context and retry dibs "+command)
		}
	}
	if command == "adopt" {
		if terminal {
			resolved, err := d.Terminal.ResolveServer(ctx, *server)
			if err != nil {
				return outputpkg.Error(out, mode, 1, err.Error(), "Check that the "+selected+" server is reachable and retry dibs adopt")
			}
			r.Terminal = &adapterpkg.TerminalPin{Server: resolved}
		}
		r.Adapter, r.Name = fs.Arg(0), fs.Arg(1)
		return app.adopt(ctx, out, mode, leasepkg.AdoptRequest{Owner: parsedOwner, TTL: ttl, Resource: r, File: file, Key: key, ProtectLiveOwner: protect, Policy: leasePolicy}, key != "")
	}
	rows := []map[string]any{}
	if !terminal && dockerErr == nil {
		found, err := app.leases.Orphans(ctx, r)
		if err != nil {
			return app.leaseError(out, mode, command, err)
		}
		for _, row := range found {
			if kind == "" || row["type"] == kind {
				rows = append(rows, row)
			}
		}
	}
	// A missing tool or stopped server makes only that terminal type unavailable.
	unavailable := map[string]string{}
	if dockerErr != nil {
		unavailable["docker"] = dockerErr.Error()
	}
	for _, name := range terminals {
		td, _ := app.resources.Lookup(name)
		resolved, err := td.Terminal.ResolveServer(ctx, *server)
		var found []map[string]any
		if err == nil {
			found, err = td.Discover(ctx, adapterpkg.ResourceRef{Adapter: name, Terminal: &adapterpkg.TerminalPin{Server: resolved}})
		}
		if err != nil {
			unavailable[name] = err.Error()
			continue
		}
		found, err = app.leases.Unleased(ctx, adapterpkg.ResourceRef{Terminal: &adapterpkg.TerminalPin{Server: resolved}}, found)
		if err != nil {
			return app.leaseError(out, mode, command, err)
		}
		rows = append(rows, found...)
	}
	for _, row := range rows {
		row["adopt"] = adoptionCommand(row)
	}
	result := map[string]any{"count": len(rows), "orphans": rows, "help": []string{"Unleased does not mean unused. Choose an owner and TTL explicitly; age alone never authorizes cleanup."}}
	if len(terminals) > 0 {
		result["unavailable"] = unavailable
	}
	for _, name := range slices.Sorted(maps.Keys(unavailable)) {
		hint := "Start the " + name + " server, or run dibs orphans --type " + name + " --server S to inventory another one"
		if name == "docker" {
			hint = "Docker resources were not inventoried; select a reachable context with --context NAME and retry dibs orphans"
		}
		result["help"] = append(result["help"].([]string), hint)
	}
	outputpkg.Emit(out, mode, result)
	return 0
}

func (app application) adopt(ctx context.Context, out io.Writer, mode outputpkg.Mode, r leasepkg.AdoptRequest, contract bool) int {
	l, done, err := app.leases.Adopt(ctx, r)
	if err != nil && contract {
		return app.contractError(out, mode, "adopt", r.Pending, err)
	}
	if err != nil {
		return app.leaseError(out, mode, "adopt", err)
	}
	live := ownerpkg.Alive
	if r.Pending != "" {
		// A pending lease's owner was not rechecked here, so report what it is now.
		live = app.owners.Check(ctx, l.Owner, time.Now().UTC()).Liveness
	}
	kube := kubeconfigFor(app.resources, l)
	next := []string{"Run dibs release " + l.ID + " to preview cleanup"}
	if path := l.ResourceRef.Path; path != "" {
		next = append([]string{"Work in " + remotepkg.ShellQuote(path) + "; never run treehouse return on it, release the lease instead"}, next...)
	}
	observed := l.Created
	if done {
		observed = time.Now().UTC()
	}
	result := map[string]any{"lease": outputpkg.LeaseView(l, live, observed, kube), "record": outputpkg.LeaseRecord(l, kube), "help": next}
	if done && !contract {
		// An unkeyed terminal retry found this owner already holding the same pinned terminal.
		result["existing"] = true
		result["help"] = []string{"This owner already holds the lease; run dibs extend " + l.ID + " --ttl D to renew it"}
	}
	if contract {
		status := map[bool]string{false: "adopted", true: "bound"}[r.Pending != ""]
		if done {
			status = "already_done"
		}
		result = contractResult(status, result)
	}
	outputpkg.Emit(out, mode, result)
	return 0
}
