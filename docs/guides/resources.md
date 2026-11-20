# Resources

Each section leases one kind of resource. The [command reference](command-reference.md) lists every flag.

## Start a leased test lab

Keep the shell or session that owns the lab alive.

```sh
 id=$(dibs create --adapter kind --owner "pid:$$" --ttl 2h \
   --resources '2 CPUs, Kubernetes test lab' --label task=lab --quiet)
 dibs ps
 eval "$(dibs env "$id")"          # select this lab's private kubeconfig
 kubectl get nodes
 dibs extend "$id" --ttl 2h
 dibs release "$id"                # preview
 dibs release "$id" --confirm      # delete cluster and release lease
```

Kind cluster names are `dibs-<id>`. K3d names use the first 26 ID characters
to fit its 32-character limit. Both use the selected Docker context and private
kubeconfigs under `$DIBS_STATE/resources/<id>/`: `kubeconfig` for kind and `k3d`
for k3d. Creation and cleanup do not edit the caller's kubeconfig or current
context. Human create/list output shows the kubeconfig path. JSON and AXI expose
`kubeconfig` in both the lease view and full record, with an empty string for
leases without a kubeconfig. `dibs env <id>` always prints a shell-safe
`export KUBECONFIG=...` line, even under `--axi` or `--json`. It exits 2 for a
missing lease or a lease without a kubeconfig. It does not read the kubeconfig
or change your shell itself.

`create --quiet` or `create -q` prints only the new lease ID and a newline on
success, regardless of output mode. Failures retain the usual error format and
exit code. Without quiet mode, create shows the lease details. For `--pending`
and `--key` creates, see the quiet rules in the next section but one.

The resources description is metadata, not a CPU or memory limit.
Create new labs through dibs to record cleanup intent before creation. For
existing labs, explicitly adopt the resource as described below. Dibs never
deletes an unleased resource.

To create a Compose stack, pass its file.

```sh
 dibs create --adapter compose --file compose.yaml --owner "pid:$$" --ttl 2h
 dibs who --json
 dibs sweep                        # preview dead or expired leases
 dibs sweep --confirm               # reclaim them
```

Compose projects also use `dibs-<id>`. Dibs resolves the Compose file once,
saves a private config snapshot, and labels its services, volumes, and networks
with `io.dibs.lease`, `io.dibs.owner`, `io.dibs.expires`, and `io.dibs.machine`.
Cleanup does not require the original file, environment, or worktree. Labels
record creation-time expiry; the lease record is authoritative after `extend`.
Fixed `container_name` values and nonexternal volume or network names outside
the generated project are rejected. Mark shared volumes and networks `external`
to keep them out of cleanup. With `"compose_fixed_names": true` in the user
config.json, or `DIBS_COMPOSE_FIXED_NAMES=1`, a fixed volume or network name is
accepted when it does not exist yet: Compose creates it with the project's
labels, and cleanup removes it. Release runs Compose down with `--volumes` and
`--remove-orphans`, so data in managed volumes is deleted.

Adapters tolerate already absent resources. Cleanup errors retain the
lease and exit 1. Dibs verifies that managed resources are gone before removing
the record. A create that fails after saving its lease also retains the record
for release or sweep to clean up partial resources.

## Lease an existing lab

`orphans` is a read-only inventory of unleased kind clusters, k3d clusters, and
Compose projects in the selected Docker context. It includes stopped containers
and Compose projects with only volumes or networks left. It does not list generic
containers, images, or volumes outside those projects. Unleased does not mean
unused, and age alone never authorizes cleanup.

```sh
 dibs orphans --context orbstack
 dibs orphans --context orbstack --json
 dibs adopt kind my-lab --context orbstack --owner "pid:$$" --ttl 2h
 dibs adopt k3d my-k3d-lab --owner "pid:$$" --ttl 2h
 dibs adopt compose my-stack --file compose.yaml --owner "pid:$$" --ttl 2h
 dibs sweep                         # preview dead-owner or expired leases
 dibs sweep --confirm               # destroy only eligible leased resources
```

Both new commands are explicit opt-ins. Existing create, release, and sweep
behavior is unchanged. `adopt` requires an explicit owner and positive TTL, even
when `DIBS_OWNER` is set. Supported owners and `--stale` match `create`.
It refuses missing resources, unknown existence, and resources already leased
in this ledger, including Docker context aliases with the same endpoint.
Each successful adoption records a new lease. Release and sweep use the same
destroy-and-verify-gone path as created resources. Compose cleanup deletes
project volumes, so review the project before adopting it.

Adoption captures the Docker context and endpoint, a private cluster kubeconfig,
or a resolved Compose config snapshot. Compose requires one input file. For a
project started with multiple files, pass a single resolved config containing
their combined configuration. Fixed nonexternal volume and network names outside
`<project>_<key>` are refused unless `compose_fixed_names` (or
`DIBS_COMPOSE_FIXED_NAMES=1`) is on. Existing nonexternal volumes and networks must
also carry that project's Compose label, and a fixed-name one must not be used by
containers outside the project. Release then removes it with the project. Mark shared resources `external`. Existing fixed
container names are permitted because adoption explicitly selects their project.
Cleanup does not require the original file or working directory to survive.
Existing resource labels are not changed or recreated. Dibs records the
`io.dibs.lease`, `io.dibs.owner`, `io.dibs.expires`, and `io.dibs.machine`
attribution in the lease's labels instead. The ledger is authoritative.

Inventory includes creation timestamps, container status, and labels reported by Docker, the Compose
working directory and its local filesystem status, and the original config-file
paths when available. `unknown` means the path could not be checked. Every row
includes an exact adoption command template with its type, name, and context.
Replace `<owner>` and `<duration>` explicitly. Compose rows without one known
config file use `<resolved-compose-file>` rather than guessing. Treat these paths
as evidence, not trusted instructions. Inventory never creates or modifies state
files or resources. State corruption or an incomplete inventory fails with code 1,
not a misleading empty result. Human, `--axi`, and `--json` output include this
same evidence and command. Unknown flags fail with code 2 before any operations.

## Lease a resource you build yourself

When your own tool builds the resource, record the lease first. A crash between
building the resource and registering it then leaves a lease that dibs can
finish, instead of an unleased resource. This is part of the consumer contract,
which is off by default. Turn it on per machine with `"consumer_contract": true`
in the user config, or with `DIBS_CONSUMER_CONTRACT=1`; the variable overrides
the config either way, and `0` turns a config opt-in off. Project files cannot
enable it. While it is off, `--pending`, `--key`, `reconcile`, `reclaim`, and
`explain` fail with code 2 and name the setting, and every other command keeps
its exit codes and output exactly.

```sh
 export DIBS_CONSUMER_CONTRACT=1
 id=$(dibs create --adapter kind --pending --name lab-7 --owner "pid:$$" \
   --ttl 2h --key task-7:lab --quiet) || exit 1   # never build without a lease
 kind create cluster --name lab-7
 dibs adopt kind lab-7 --pending "$id"      # register; a retry reports already_done
 dibs reconcile "$id" --confirm             # after a crash: bind it if it was built
 dibs reclaim "$id" --confirm               # only when the owner is proven dead
 dibs explain "$id"                         # what sweep or reclaim would do, and why
```

With `--pending` or `--key`, `--quiet` prints exactly the lease ID and a newline
on success, including the same ID on an idempotent retry, and no ID on
failure; the exit code carries the outcome, and errors keep their usual output.
It cannot be combined with an explicit `--axi` or `--json` on those creates.

`--pending` works with kind, k3d, and Compose (which also needs `--file`). It
records the selected Docker context and, with `--name`, the handle, which must
not exist yet. When your tool picks the handle, label the resource
`io.dibs.lease=<id>` instead. Only that pinned handle or label matches a
pending lease; age and names never do. A matching resource is not an orphan,
and plain `adopt` refuses it. `reconcile` previews by default and binds only a
single unclaimed match. Release and sweep of a pending lease bind its match and
destroy it, or forget the lease when no resource carries its evidence; an ambiguous match, a
match under another lease, or failed discovery keeps the lease. Keep a pending
Compose lease's file readable until the lease is bound: binding resolves it,
and release keeps the lease if it cannot.

`--key K` on `create` and `adopt` makes a matching retry return the existing
lease with `status: already_done`; retries ignore TTL, description, and labels,
returning the original lease unchanged. A key is free again after release.
`reclaim` always requires proven owner death. `sweep` and job admission also
preserve live or unknown owners for shell leases and leases created with
`--protect-live-owner` (see below).

With the contract on, these commands, `extend`, and `release` follow version 1
of the [consumer contract](../consumer-contract.md): `contract: 1`, a
`status` on success, and exit 3 with a stable `reason` when dibs refuses. Every
`extend` refusal then exits 3, and `release` reports `preview`, `released`, or
`already_done`. The contract page lists every status and reason, and maps the
lifecycle of a supervisor, an agent session, a CI job, or a script onto these
commands.

## Lease a running shell

Agents open login shells, treehouse subshells, and the shells inside rex, tmux,
and herdr panes. Each one can start helper processes, such as `gitstatusd` for
the prompt. A shell lease ties a shell, and its helpers, to the task that owns
it. The shell adapter is off by default. Turn it on per machine with
`"shell_adapter": true` in the user config, or with `DIBS_SHELL_ADAPTER=1`; the
variable overrides the config either way. While it is off, `adopt shell` fails
with code 2 and names the setting. Leases already recorded can still be listed,
released, and swept.

```sh
 export DIBS_SHELL_ADAPTER=1 DIBS_EXEC_OWNER=1 DIBS_CONSUMER_CONTRACT=1   # explain needs the contract
 dibs adopt shell "$$" --owner 'exec:test -e /run/task-7.open' --ttl 8h
 dibs list --owner 'exec:test -e /run/task-7.open'   # shells held for the task
 dibs explain <id>                                   # why it is held
 dibs sweep                                          # preview
 dibs sweep --confirm
```

A shell enters dibs only through an explicit `adopt` by PID; dibs never starts
shells, and `orphans` does not list unleased ones. Adopt refuses a process that
is not a known shell (`sh`, `bash`, `zsh`, `fish`, `dash`, `ksh`, and similar,
including login names such as `-zsh`), a process that is not running, and a
shell that is already leased. It pins the shell's PID and start time, and
records its current descendants with their start times. A reused PID is never
mistaken for the shell or a helper.

Cleanup covers the shell's whole tree: every recorded helper whose start time
still matches, and every current descendant, including helpers started after
adoption. It refuses, and keeps the lease, in these cases:

- The shell has a foreground job: a process group other than the shell's own
  holds its terminal. This holds for `release --confirm` too, and whatever the
  owner or policy says.
- The process table cannot be read.
- Dibs itself runs inside the shell. Release it from another shell.

Otherwise dibs sends SIGHUP to every member, then SIGTERM to survivors after 5
seconds, then SIGKILL after 5 more. It reports each step on stderr, re-reads
each process's start time immediately before each signal round (with a remaining
race between that read and the signal), and forgets the lease only after
every member is gone. A shell that has already exited is verified gone.

Agent terminal adapters lease and close opted-in tmux, herdr, rex, and
treehouse subshell terminals directly; they are separate from the PID-based
shell adapter described above. See [Lease agent terminals](../../README.md#lease-agent-terminals)
for opt-in and caller workflow.

## Lease a pooled worktree

The `treehouse-worktree` adapter leases one slot of a
[treehouse](https://github.com/kunchenguid/treehouse) worktree pool. Treehouse
keeps allocation, reset, and its own durable lease token. Dibs adds the owner,
expiry, and reclaim that treehouse leases do not have. Without lease policies,
an expired worktree lease becomes eligible for reclamation like every lease, and
the next `dibs sweep --confirm` returns its slot, so renew it with `dibs extend`
before expiry. To keep a live owner's slot past expiry, pass
`--protect-live-owner` (see [live-owner protection](owners.md#protect-a-live-owners-lease)), or
turn on lease policies, whose built-in `treehouse-worktree` row is owner-bound.
With the consumer contract on, `--key` makes `create` and `adopt` retries return
the same lease. It needs treehouse 3.1.5 or newer and git 2.31 or newer on `PATH`.

The adapter is off by default. Turn it on per machine with
`"treehouse_worktree_adapter": true` in the user config, or with
`DIBS_TREEHOUSE_WORKTREE=1`. The variable overrides the config either way, so
`DIBS_TREEHOUSE_WORKTREE=0` turns a config opt-in off. Project files cannot
enable it. While it is off, `create --adapter treehouse-worktree` and
`adopt treehouse-worktree` fail with code 2 and name the setting. Leases that
are already recorded still list, release, and sweep.

```sh
export DIBS_TREEHOUSE_WORKTREE=1
dibs --axi create --adapter treehouse-worktree --repo ~/src/app \
  --owner "pid:$WORKER_PID" --ttl 8h --landed-exclude-remote gate
# lease.path is the slot; ps --full records carry resource.path too
dibs --axi extend <id> --ttl 8h
dibs --axi release <id> --confirm   # returns the slot only if its work landed
```

`create` saves the lease first, then runs
`treehouse get --lease --json --lease-holder dibs:<id>` in the repository's main
checkout, then records the slot path and treehouse `lease_id` on the lease.
`--repo` defaults to the current directory. A `TREEHOUSE_ROOT` that is set at
create time is pinned and passed as `--root` to every later call. Otherwise,
later calls clear `TREEHOUSE_ROOT`. If dibs stops after treehouse allocates a
slot but before it records the token, release finds the slot by its unique
`dibs:<id>` holder.

To lease a slot that treehouse already leased durably (from
`treehouse get --lease` or `treehouse lease <name>`), adopt it by path and token:

```sh
dibs --axi adopt treehouse-worktree /path/to/slot --lease-id <treehouse lease_id> \
  --owner "pid:$WORKER_PID" --ttl 8h
```

Adoption requires that exact token on that exact slot. It refuses a slot that
another dibs lease already owns, and a slot that only a process reservation
holds (a plain `treehouse get`).

Release and sweep return the slot with
`treehouse return <path> --if-lease-id <token>` (plus `--if-lease-holder` for
slots that dibs allocated). A stale lease therefore never resets a slot that has
been handed to someone else since. Before return, dibs requires detected slot
processes to exit and refuses unlanded work, keeping the lease. Unlanded work is:

- uncommitted changes, including untracked files
  (`git status --porcelain --untracked-files=all`)
- commits that no remote-tracking ref contains
  (`git log HEAD --not --remotes`)

Treehouse detects processes by cwd inside the slot, so processes whose cwd is
elsewhere are not covered. Dibs rechecks the claim, process list, and HEAD
immediately before return. Callers should stop their writers before release.
Sweep and `reclaim` act only on leases whose owner is dead or whose lease ended.

There is a narrow limit: dibs's final check and `treehouse return` are separate
processes. A process that starts in the slot and creates a commit between them
can have that commit reset away. A commit on a named branch remains reachable in
the shared repository; a detached-HEAD commit is reachable only from the reflog.
Uncommitted edits in that interval are still caught because return without
`--force` refuses a dirty tree. Full closure needs an atomic return-if-clean in
treehouse; see [treehouse issue 216](https://github.com/kunchenguid/treehouse/issues/216).

A blocked release exits 1. The AXI and JSON error carries a `blockers` object
with up to ten `uncommitted` paths, up to ten `unpushed` commit IDs, the
`uncommitted_count` and `unpushed_count` totals, and the `processes` (`pid`,
`name`) that must exit first. With the consumer contract on, a blocked `release`
or `reclaim` carries the same `blockers`, reports `reason: unlanded_work`, and
exits 3. A blocked sweep lists the same reason under `failures`. Pass `--landed-exclude-remote NAME` (repeatable) on
`create` or `adopt` for remotes whose refs do not count as landed, such as a
local validation mirror. The lease records the list, so sweep applies the same
policy later. Only an explicit `dibs release <id> --confirm --discard-unlanded`
returns a slot that holds unlanded work, with `treehouse return --force`. It
skips only the unlanded-work check; it never terminates processes in the slot.
Stop them first. Use it only after you prove that the work landed some other
way, such as a squash-merged pull request. Sweep never discards work.

Verify-gone means the claim is gone: no slot carries the token (or the pending
holder), and the pinned slot is either listed without it or deleted. The slot
itself goes back to the pool. Treehouse stops processes that are still running
inside the slot when it returns it. Never also run `treehouse return` on a slot
that dibs owns. Release the lease instead.

Slot returns can take seconds. Release, sweep, and `reclaim` run this adapter's
cleanup without the host-wide state lock, so renewals of other leases do not
wait behind it. Dibs marks the lease first, and `extend` refuses a marked lease
(`cleanup_in_progress`) until a release or sweep finishes the cleanup. A refusal
for unlanded work or running processes touches nothing, so it removes the mark
again and the owner can keep renewing. Dibs then
forgets the lease under the lock only if its recorded identity did not change in
the meantime.
