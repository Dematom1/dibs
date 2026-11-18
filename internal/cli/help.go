package cli

import (
	"io"
	"strings"

	treehousepkg "github.com/Dematom1/dibs/internal/adapter/treehouseworktree"

	outputpkg "github.com/Dematom1/dibs/internal/output"
)

const exitCodes = `Exit codes: 0 success (including no-ops), 1 operation failed, 2 invalid usage.
dibs job: child's exit code on completion; 124 deadline killed child;
75 (EX_TEMPFAIL) no slot within wait. Dibs's own 1/2 apply only when no child
was started. Signal termination returns 128 + signal number. Helper/wait
failures after start use 125/126 and diagnostics on stderr.
76 is SSH transport/protocol failure with unknown remote result (stderr).
Child exit 255 is preserved and is not confused with SSH failure.`

const contractCodes = `Consumer contract 1 is off until DIBS_CONSUMER_CONTRACT=1 or "consumer_contract":
true in the user config. While off, reconcile, reclaim, explain, --pending and
--key exit 2 naming the setting, and extend and release behave as described above.
While on, these commands and extend and release follow contract 1:
exit 0 success with status; 1 operation failed, records retained; 2 usage;
3 refusal, nothing changed, with a stable reason: not_found, wrong_machine,
not_pending, already_leased, key_conflict, handle_exists, handle_mismatch,
resource_absent, resource_unknown, ambiguous, owner_alive, owner_unknown,
expired, owner_dead, job_deadline, max_lifetime, grace, resource_busy,
cleanup_in_progress, unlanded_work.
Extend success is renewed or reattached.
Exit 1 is only for operation failures. Output carries contract: 1.
See docs/consumer-contract.md.`

const cadence = `Touch heartbeat files at most every staleness/3. Extend a lease at the latest
when one third of its TTL remains. Owner death OR expiry makes an unprotected
lease reclaimable, so its live owner can lose its resources by missing either
cadence. With --protect-live-owner, only proven owner death reclaims; a late
extend with a live owner reattaches when the resource is still present.`

const policies = `Lease policies are off until DIBS_LEASE_POLICIES=1 or "lease_policies": true.
While on, the policies table in the user config picks each new lease's rule by
resource kind, or kind/owner kind: binding time (TTL), owner (only owner death
ends it), or hybrid (owner-bound plus idle or max_lifetime); ttl, renew,
max_lifetime, grace, idle, reclaim_live_owner and action destroy|report. Shells
never reclaim on TTL while their owner is alive or unknown. Idle/max_lifetime
reclaim a live owner only with reclaim_live_owner: true on a hybrid row; an
unknown owner is never reclaimed. The lease records its policy.
--ttl must fit max_lifetime; --idle D and --max-lifetime D may only tighten the
row. Off, --idle and --max-lifetime exit 2 naming the setting. Built-in
kind/scope rows renew a scope's shells, terminals and worktrees every 30m, with
a 10m grace.`

var commandHelp = map[string]string{
	"adopt": `dibs adopt <kind|k3d|compose> <name> --owner KIND:ID --ttl D
  [--context NAME] [--file PATH (required for compose)] [--stale 15m]
  [--protect-live-owner] [--key K] [--idle D] [--max-lifetime D] [--full] [--axi | --json]
dibs adopt shell <pid> --owner KIND:ID --ttl D [--key K] [--idle D] [--max-lifetime D]
dibs adopt <tmux|herdr|rex|treehouse-subshell> <id> --owner KIND:ID --ttl D [--server S] [--key K]
dibs adopt <kind|k3d|compose> <handle> --pending ID [--file PATH] [--axi | --json]
Explicitly lease an existing resource. Existence must be verified and no lease
may already own it. Captures cleanup configuration; does not recreate resources.
Owner and positive TTL are required; with lease policies on, the policy
supplies the TTL. Release/sweep keep their preview defaults.
shell (DIBS_SHELL_ADAPTER=1 or "shell_adapter": true) leases a running shell by
PID and start time, with its descendants such as gitstatusd. Cleanup refuses a
shell with a foreground job, sends SIGHUP, then SIGTERM and SIGKILL after 5s
each, reports each step on stderr, and verifies every member gone.
Terminal types (terminal_adapters in config.json or DIBS_TERMINAL_ADAPTERS) pin
the terminal's process so a reused ID is never reclaimed. Like shells they are
owner-bound: only proven owner death reclaims them. Cleanup hangs up the shell,
then closes the pane or block. A same-owner retry returns existing: true.
--key K: retries match owner, protection, adapter, mode, resource, Docker scope or terminal server, and Compose file; TTL, description, and labels are ignored.
--protect-live-owner: only proven owner death reclaims; expiry alone never does.
Requires DIBS_LIVE_OWNER_PROTECTION=1 or "live_owner_protection": true.
--pending ID registers a resource the caller created for a pending lease (see
dibs create --pending); owner, TTL and context come from that lease. A retry
returns status already_done. Statuses: adopted, bound, already_done.
Examples:
  dibs adopt kind my-lab --owner pid:1234 --ttl 1h
  dibs adopt compose my-stack --file compose.yaml --owner pid:1234 --ttl 2h
  dibs adopt kind lab-7 --pending <id>
  dibs adopt shell "$$" --owner 'exec:test -e /tmp/task-7.open' --ttl 8h
  dibs adopt tmux %3 --owner pid:1234 --ttl 4h`,
	"reconcile": `dibs reconcile [<id>] [--confirm] [--axi | --json]
Resolve pending leases after a crash between creating a resource and dibs adopt
--pending. A resource matches only by evidence recorded before it existed: the
handle pinned with create --name, or the label io.dibs.lease=<id>. Preview by
default. --confirm binds each single unclaimed match. Per lease: bindable, bound,
absent (no resource carries the handle or label; check for one built without
that evidence before building again), ambiguous (several matches, never
bound), conflict (match under another lease), unknown (discovery failed).
With <id>: a lease that is no longer pending reports already_done; ambiguous
and conflict exit 3. Unknown exits 1. Age and names never decide a match.
Examples:
  dibs reconcile
  dibs reconcile <id> --confirm`,
	"scope": `dibs scope start [--key K] [--owner KIND:ID] [--ttl 8h] [--stale 15m] [--axi | --json]
dibs scope end <id> [--confirm] [--axi | --json]
dibs scope show <id> | --key K [--full] [--axi | --json]
A scope is a lease with no external resource that groups the leases of one unit
of work and owns them: lease with --owner scope:<id> on create or adopt. Needs
DIBS_SCOPES=1 or "scopes": true in the user config; off, every scope command
and the scope owner exit 2 naming the setting.
start gets or creates a scope. --key K is the caller's own id for the work: the
same key and owner return the same live scope (status already_done), another
owner is key_conflict, and an ended scope is scope_ended. Owner and TTL default
as in create; a scope cannot be owned by another scope. Renew it with dibs
extend <id>. Statuses: created, already_done.
scope:<id> is alive while the scope exists, is unexpired and its owner is alive;
dead once the scope is absent or itself reclaimable; unknown while its owner is.
end previews the leases the scope owns. --confirm expires the scope, releases
each of them newest first with the ordinary release, and forgets the scope once
none remain. A failed release keeps that lease and the scope (exit 1, failures);
rerun to retry. Statuses: preview, ended, already_done.
show prints the scope and each lease it owns with its owner liveness.
A scope with leases is never forgotten: sweep holds it (scope_members) until its
leases are gone, and release and reclaim refuse it (exit 3, scope_members).
Scope output follows consumer contract 1 whether or not that contract is on:
contract: 1 and a status; exit 0 success, 1 operation failed with records
retained, 2 usage, 3 refusal with nothing changed and a stable reason:
key_conflict, scope_ended, not_scope, not_found, wrong_machine.
Examples:
  dibs scope start --key build-42 --owner 'exec:test -e /tmp/build-42.open'
  dibs create --adapter kind --owner scope:<id> --ttl 2h
  dibs scope end <id> --confirm`,
	"reclaim": `dibs reclaim <id> [--confirm] [--axi | --json]
Reclaim one lease only when its owner is proven dead on this machine. Preview by
default. --confirm destroys the resource, verifies it is gone, then forgets the
lease. A job lease whose process-group leader is already gone is released
without signaling, so its descendants are not verified gone. An alive owner
is refused (owner_alive) even after expiry; unknown liveness is refused
(owner_unknown). Expiry-driven reclaim stays with sweep. A dead owner inside its
policy's grace period is refused (grace), and so is a resource whose adapter
reports work in progress (resource_busy) or cannot read it (resource_unknown).
A lease already gone is status already_done. Statuses: preview, reclaimed,
already_done.
Examples:
  dibs reclaim <id>
  dibs reclaim <id> --confirm`,
	"explain": `dibs explain <id> [--full] [--axi | --json]
Read-only: the lease, its owner liveness, expiry, machine, whether sweep or
reclaim would act now, and why. Status explained; a missing lease is not_found.
With lease policies on, for a lease that recorded a policy, or for a resource
whose adapter reports activity, it also shows policy (row, source, binding,
ttl, renew, max_lifetime, grace, idle, action), trigger, held_because,
reclaim_at, remaining, renew_by, and activity (busy, idle, members).
Examples:
  dibs explain <id>
  dibs explain <id> --json`,
	"orphans": `dibs orphans [--context NAME] [--type T] [--server S] [--full] [--axi | --json]
Read-only inventory of unleased kind, k3d and Compose resources in the selected
Docker context, plus opted-in terminals. --type limits it to one type; a terminal
type skips Docker. Includes evidence and adoption command templates.
Unleased does not mean unused: age never authorizes deletion. No state is written.
Examples:
  dibs orphans
  dibs orphans --context orbstack --json
  dibs orphans --type rex`,
	"setup": `dibs setup hooks [--remove] [--axi | --json]
Install or remove managed Claude Code, Codex, Pi and OpenCode integrations.
Sets DIBS_AXI=1 in agent shell environments. Repeated setup is a no-op.
Examples:
  dibs setup hooks
  dibs setup hooks --remove`,
	"env": `dibs env <id>
Print a shell-safe export for a kind/k3d lease's private kubeconfig.
This command always prints shell text, even with --axi or --json.
Examples:
  eval "$(dibs env <id>)"
  dibs env <id>`,
	"create": `dibs create [--owner KIND:ID] [--ttl 8h] [--stale 15m]
  [--resources TEXT] [--label key=value ...] [--adapter {{adapters}}]
  [--file PATH (required for compose)] [--protect-live-owner] [--key K] [--pending [--name HANDLE]]
  [--idle D] [--max-lifetime D] [--quiet | -q] [--axi | --json]
Owner defaults to DIBS_OWNER, then pid:<parent-pid>. pid and session pin
process start time; session:<holder-pid> follows a session-holding process,
not a Unix SID. heartbeat:<path> must already be a fresh regular file.
exec:<command> (DIBS_EXEC_OWNER=1 or "exec_owner": true) runs the command with
/bin/sh from / with a 10s timeout: exit 0 alive, 1-125 dead, anything else unknown.
kind and k3d create uniquely named clusters; kind uses a private kubeconfig.
compose creates
a uniquely named project with a durable resolved config. none records metadata
only and performs no external cleanup. up is an alias.
--protect-live-owner: sweep never reclaims while the owner is alive or unknown,
even after expiry; only proven owner death reclaims. Use the worker's own owner.
Requires DIBS_LIVE_OWNER_PROTECTION=1 or "live_owner_protection": true.
--key K and --pending need the consumer contract (DIBS_CONSUMER_CONTRACT=1).
--key K: retries match owner, protection, adapter, mode, pinned handle, Docker scope, and Compose file; TTL, description, and labels are ignored.
With --pending or --key, --quiet prints only the lease ID (including on retry);
exit codes distinguish success from failure. --quiet cannot combine with --axi
or --json for those contract creates. Without these flags it keeps its usual behavior.
--pending records the lease before the caller creates a kind, k3d or Compose
resource in the selected Docker context; the caller then stamps the label
io.dibs.lease=<id> or uses the handle pinned with --name (which must not exist
yet), and runs dibs adopt <type> <handle> --pending <id>. Release and sweep
of a pending lease bind its single match first, so a crash still reclaims.
Statuses: created, recorded, already_done.
Examples:
  dibs create --owner pid:1234 --ttl 2h --resources '2 CPUs, test lab'
  dibs create --adapter kind --owner session:1234 --ttl 1h
  dibs create --adapter compose --file compose.yaml --label task=lab
  dibs create --adapter kind --pending --name lab-7 --key task-7:lab --quiet`,
	"list": `dibs list [--owner KIND:ID] [--machine NAME] [--full] [--axi | --json]
List every recorded lease with owner liveness, age, expiry, machine, resource
identity, and description. --json is always complete. AXI defaults to 100
compact rows and 500-character text previews; --full shows all fields, records,
text and matching leases. Counts and --full hints identify truncated output.
Aliases: ls, ps, who. No arguments shows the live home view.
Examples:
  dibs ps
  dibs who --owner pid:1234 --json`,
	"extend": `dibs extend <id> --ttl D [--json]
Set expiry to now + D (positive Go duration). Only an unexpired lease with a
verified live owner on this machine can be extended. It cannot be resurrected.
A --protect-live-owner lease with a live owner also renews after expiry when its
resource is still present (status reattached). A protected or owner-bound
policy lease refused for reason owner_dead, owner_unknown, resource_absent,
resource_unknown or max_lifetime exits 3; every other refusal exits 1. A refusal
leaves the lease unchanged. A lease with a policy max_lifetime never extends
past it: a time-bound lease expires at its cap and is refused as expired, and an
owner-bound or hybrid lease at its cap is refused with max_lifetime.
With the consumer contract on, every refusal exits 3 with a reason: also
expired, wrong_machine, job_deadline and not_found; exit 1 is then only for
operation failures, and success carries contract: 1 and status renewed.
Examples:
  dibs extend <id> --ttl 2h
  dibs extend <id> --ttl 30m --json`,
	"release": `dibs release <id> [--confirm] [--json]
Preview by default. --confirm destroys managed resources (including Compose
volumes) and removes the lease only on success. Already absent is success.
A failed cleanup retains the lease for retry. down is an alias.
With the consumer contract on, status is preview, released or already_done,
and wrong_machine or an ambiguous pending lease refuses with exit 3.
Examples:
  dibs release <id>
  dibs release <id> --confirm`,
	"sweep": `dibs sweep [--confirm] [--machine NAME] [--json]
Preview by default. --confirm reclaims on owner dead OR expiry. Unknown owners
are retained unless expired, except shell leases which remain owner-bound. --protect-live-owner leases are reclaimed only on
proven owner death. Foreign-machine leases are always retained, even
when expired. Failed cleanup retains its lease. No unleased resources are touched.
With lease policies, a lease's recorded policy also decides: owner-bound leases
ignore expiry, and max_lifetime and idle timeouts may end leases. For shells,
TTL never reclaims a live or unknown owner; idle/max_lifetime require
reclaim_live_owner: true and a proven-live owner. held lists leases whose
condition fired but are kept: grace, resource_busy, resource_unknown,
live_owner, or report_only. A busy resource is never reclaimed.
Run this command from a host timer for automatic reclamation; no daemon runs.
Examples:
  dibs sweep
  dibs sweep --confirm --json`,
	"agent": `dibs agent
Internal, version-checked JSON protocol over SSH stdin/stdout. EOF on stdin
cancels the session's job and releases its remote reservation. No daemon.
Use dibs job --machine <name> -- <cmd> rather than invoking the protocol.
Examples:
  dibs job --machine lenovo -- go test ./...
  dibs ps --machine lenovo`,
	"machine": `dibs machine install <name> --file <binary> [--json]
Install or upgrade a trusted dibs binary built for the remote OS/architecture.
The remote checks --version before atomic replacement; mismatches retain the
existing binary. Machine config requires host, user, absolute workdir and agent
paths; optional port and identity_file. SSH config aliases are supported.
No source, worktree, environment, credentials, or build tools are synchronized.
Examples:
  GOOS=linux GOARCH=amd64 go build -o /tmp/dibs-linux .
  dibs machine install lenovo --file /tmp/dibs-linux`,
	"job": `dibs job [--machine NAME] [--cores N --memory M --class C --timeout D --wait W] -- <cmd...>
Local by default; --machine selects a configured SSH host. SSH admission,
classes, budget, owner room, state, deadlines and cleanup are remote-authoritative.
Remote jobs use the configured workdir and remote environment; stdin is /dev/null
(the SSH stdin channel is reserved for session liveness). Disconnect cancels the
remote process group and reclaims its lease. No worktree transfer or daemon.
Reserve logical cores and memory minus owner room, FIFO
(no backfill). Defaults: --class default (1 core, up to 1GiB, timeout 1h),
--wait 30m. --cores, --memory and --timeout override class defaults.
Memory: integer bytes or KiB/MiB/GiB/KB/MB/GB. Durations: positive Go durations;
--wait 0 tries once. Oversized requests fail with 2 before starting a child.
DIBS_CONFIG selects strict JSON config; default ~/.config/dibs/config.json
(or $XDG_CONFIG_HOME/dibs/config.json). Budget defaults derive from the host.
Jobs run in a process group owned by the dibs job PID. Use noninteractive
commands; no terminal job-control
handoff is provided. Deadline
or cancellation terminates the group (TERM, then KILL after 1s); sweep reclaims
after owner death or expiry. Children that call setsid can escape the group.
Reservations are cooperative admission, not hard CPU/memory limits.
Child stdin/stdout/stderr pass through unchanged, including with --json;
--json applies only to pre-start errors. Post-start cleanup errors go to stderr
and retain the lease, never replacing the child's exit code.
Examples:
  dibs job --cores 2 --memory 2GiB --timeout 10m -- go test ./...
  dibs job --class suite --wait 0 -- just check`,
}

// scopeHelp joins the help only while scopes are on, so default help bytes stay unchanged.
var scopeHelp = map[string]string{
	"": `Scopes (DIBS_SCOPES=1 or "scopes": true): scope start, scope end, scope show.`,
	"create": `scope:<id> (DIBS_SCOPES=1 or "scopes": true) makes the lease a member of that
scope, owned by it: alive while the scope exists, is unexpired and its owner is
alive; dead once it is absent or ended. dibs scope end <id> --confirm releases it.`,
	"adopt": `--owner scope:<id> (DIBS_SCOPES=1 or "scopes": true) makes the lease a member
of that scope, owned by it; dibs scope end <id> --confirm releases it.`,
}

var worktreeHelp = map[string]string{
	"adopt": `dibs adopt treehouse-worktree <slot-path> --lease-id ID --owner KIND:ID --ttl D
  [--landed-exclude-remote NAME ...] [--key K] [--protect-live-owner]
  [--idle D] [--max-lifetime D]
registers a slot treehouse already leased durably, verified by its exact token.
  dibs adopt treehouse-worktree /pool/app-1a2b/3/app --lease-id <token> --owner pid:1234 --ttl 8h`,
	"create": `treehouse-worktree: [--repo DIR (default .)] [--landed-exclude-remote NAME ...]
leases a treehouse pool slot (lease.path) and returns it only after its work
landed on a remote; unlanded work keeps the lease.
  dibs create --adapter treehouse-worktree --repo ~/src/app --ttl 8h`,
	"release": `[--discard-unlanded] with --confirm discards uncommitted or unpushed work in
a treehouse-worktree slot. It never terminates running processes; stop them first.
Without it, unlanded work blocks release (exit 3, reason unlanded_work, with the
consumer contract on). Sweep and reclaim never discard.`,
	"extend": `A treehouse-worktree lease whose cleanup started is refused until release
finishes it (cleanup_in_progress; exit 3 with the consumer contract on).`,
}

func help(command string) string {
	body, ok := commandHelp[command]
	if !ok {
		body = `dibs: owner-and-expiry leases for local resources (macOS and Linux).
Commands: create (up), adopt, orphans, list (ls, ps, who), extend, release (down), sweep, job,
reconcile, reclaim, explain, env, setup hooks, agent (internal SSH protocol), machine install.
Global flags: --axi (agents), --json (scripts), --help. No --human flag.
Human output on terminals; AXI when DIBS_AXI=1 or stdout is not a TTY.
--axi and --json are mutually exclusive; either overrides DIBS_AXI.
Version: --version, -v, -V.
Environment: DIBS_OWNER; DIBS_STATE (default $XDG_STATE_HOME/dibs or
~/.local/state/dibs). State is local to one host and one Unix user.
Examples:
  dibs
  dibs create --adapter kind --ttl 1h --resources '2 CPUs, test lab'
  dibs sweep --confirm
Run dibs <command> --help for command flags.`
	}
	adapters := newAdapters()
	body = strings.ReplaceAll(body, "{{adapters}}", strings.Join(adapters.CreateNames(), "|"))
	// Opt-in help appears only once the adapter is on, so default help bytes stay unchanged.
	if d, _ := adapters.Lookup(treehousepkg.Name); d.CreateAllowed && worktreeHelp[command] != "" {
		body += "\n" + worktreeHelp[command]
	}
	if scopesEnabled() && scopeHelp[command] != "" {
		body += "\n" + scopeHelp[command]
	}
	if command == "create" || command == "extend" {
		body += "\n\n" + cadence
	}
	if command == "create" || command == "adopt" {
		body += "\n\n" + policies
	}
	if command != "agent" {
		body += "\nOutput: --axi for agents, --json for scripts (mutually exclusive).\nDefault: human on terminals; AXI for pipes or DIBS_AXI=1.\nLease commands: --full restores complete AXI records/text; JSON is always complete."
	}
	switch command {
	case "create", "adopt", "reconcile", "reclaim", "explain", "extend", "release":
		body += "\n\n" + contractCodes
	}
	return body + "\n\n" + exitCodes + "\n"
}

func emitHelp(w io.Writer, mode outputpkg.Mode, command string) {
	outputpkg.Help(w, mode, help(command))
}
