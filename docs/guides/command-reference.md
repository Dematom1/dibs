# Reference

## Command reference

| Command | Behavior |
| --- | --- |
| `dibs` | Live home view with executable path, description, lease count, and hints |
| `dibs create` or `dibs up` | Create a lease, optionally with `--adapter kind`, `k3d`, `compose`, or the opt-in `treehouse-worktree` (`--repo`, `--landed-exclude-remote`); with the consumer contract, `--pending` records it before you build the resource and `--key` makes retries idempotent |
| `dibs adopt <type> <name> --owner KIND:ID --ttl D` | Lease an existing kind, k3d, or Compose resource after verifying existence |
| `dibs adopt <type> <handle> --pending ID` | Consumer contract: register the resource you built for a pending lease |
| `dibs adopt shell <pid> --owner KIND:ID` | Lease a running shell and its helpers by PID and start time (`shell_adapter`) |
| `dibs adopt treehouse-worktree <path> --lease-id ID --owner KIND:ID --ttl D` | Lease a durably leased treehouse slot after verifying its token (`treehouse_worktree_adapter`) |
| `dibs reconcile [ID] [--confirm]` | Consumer contract: find and bind resources built for pending leases after a crash |
| `dibs reclaim ID [--confirm]` | Consumer contract: reclaim one lease only when its owner is proven dead |
| `dibs explain ID` | Consumer contract: owner liveness, expiry, and whether sweep or reclaim would act, and why |
| `dibs orphans [--context NAME]` | Read-only unleased-resource evidence and adoption command templates |
| `dibs list`, `ls`, `ps`, or `who` | Owner, liveness, age, expiry, machine, and resource description for every lease |
| `dibs env <id>` | Print a shell-safe kubeconfig export for a kind or k3d lease |
| `dibs setup hooks [--remove]` | Install or remove managed agent integrations |
| `dibs extend <id> --ttl D` | Renew an unexpired lease with a live owner; a protected lease may also reattach after expiry |
| `dibs release <id>` or `down` | Preview cleanup; `--confirm` performs it; `--discard-unlanded` lets a confirmed worktree release discard unlanded work |
| `dibs sweep` | Preview reclaim of dead-owner or expired leases; `--confirm` performs it |
| `dibs job [--machine NAME] [flags] -- <cmd...>` | FIFO machine-budget admission, deadline, and child exit-code passthrough |
| `dibs machine install <name> --file <binary>` | Version-checked SSH agent installation or upgrade |
| `dibs agent` | Internal SSH protocol with session-as-liveness |

`list` and `sweep` also accept `--machine NAME` to operate on that SSH machine.

Commands accept `--help`, `--axi`, and `--json`, except the internal `agent`
protocol. Human output is the terminal default. AXI and JSON have explicit empty
states and structured errors on stdout with short help pointers. Structured errors
include a symbolic `code` and the numeric `exit_code`. Codes are
`VALIDATION_ERROR`, `COMMAND_FAILED`, and `NO_CAPACITY`; actual exit codes do not
change. Refusals that exit 3 also include `reason`, and consumer contract output
includes `contract`. Explicit help requests also use structured output in these modes.

AXI's compact list includes lease identity, owner/liveness, machine/expiry,
resource description, exact job cores/memory bytes, and the kubeconfig path.
`--full` restores labels, class, and full records. Labels remain native objects
in both complete formats. JSON always includes the complete records. Both modes
include numeric machine-budget fields in bytes.
`leases[].memory` retains its byte-string representation for scripts;
`leases[].memory_bytes` provides the same value as an integer. Human output uses
GiB, MiB, or smaller readable units. Budget usage includes all retained local job
reservations, even when an owner filter hides a row or failed cleanup retains it.
A broken job config leaves lease listing available with a budget-unavailable note.

`job` child output passes through unchanged in every mode. Post-start cleanup
diagnostics and resource tool diagnostics go to stderr. Exit codes and stream
separation do not depend on output mode. `env` and `create --quiet` print shell
text and an ID respectively, rather than structured success output.
There are no prompts. Unknown dibs arguments
and flags fail with code 2 and help before state or resource operations run;
everything after a job's required `--` belongs to the child.

`create --adapter none` records a metadata-only lease. It does not manage or
delete an external resource. Labels are repeatable `--label key=value` metadata.
`list --owner KIND:ID` filters by canonical owner identity, resolving relative
heartbeat paths, numeric PID spellings, and PR selector forms (lowercased
`HOST/OWNER/REPO#N`) without checking owner liveness.

### AXI exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success, including already absent releases and empty sweeps |
| `1` | Dibs operation failed; cleanup records are retained on resource failure |
| `2` | Invalid command, argument, or flag; no child was started |
| `3` | `dibs extend` refused a protected lease; with the consumer contract on, any refusal by a contract command, `extend`, or `release`. Nothing changed; see `reason` |
| Child's code | `dibs job` when its child runs to completion |
| `124` | `dibs job` deadline fired before dibs confirmed the child had exited. dibs stops the child, but if it cannot verify completion the child may already have exited (coreutils `timeout` convention) |
| `75` | `dibs job`: no slot frees within `--wait`, `EX_TEMPFAIL` |
| `128 + signal` | Child terminated by a signal, e.g. 143 for SIGTERM |
| `125` | Job child started, but its wait status could not be obtained |
| `126` | `dibs job` could not execute the child, so no child ran |
| `76` | SSH transport or protocol failed, with the stderr line `SSH transport/protocol failed; remote result unknown`. A remote child that exits 76 itself returns 76 with its authoritative result and no such line |

`dibs job` uses dibs's own codes 1 and 2 only when no user child was started.
Child completion passes through the child's code even when it overlaps these
numbers, including 1, 2, 75, and 124. `dibs job --help` documents this contract.
After start, cleanup errors never override the child's result. Signals and
startup failures of the gated execution helper may return 125 or 126; a helper
execution failure is reported on stderr.

### Demo expectations

On a terminal, `dibs ps` starts with a `jobs:` budget summary and then an aligned
lease table, or `No leases.`. The no-args home view also names the executable and
describes dibs. A kind or k3d lease includes its kubeconfig path. Job rows show
cores and readable memory, and expiries read `in 1h0m0s` rather than a timestamp.

For a 14-core, 32-GiB configuration with 3 cores and 8 GiB of owner room, an empty
machine reports:

```text
jobs: 0 of 11 cores, 0 B of 24.0 GiB in use; 3 cores / 8.0 GiB kept free for you
No leases.
```

With the same config, `dibs job --cores 999 -- true` exits 2 without starting a
child. Its human output is exactly two lines:

```text
Error: requested 999 cores; usable 11 after owner room 3
Try --cores 11 or set DIBS_CONFIG
```

AXI keeps the same message under `error:`, `code: VALIDATION_ERROR`,
`exit_code: 2`, and a short `help` array. Piped
`dibs ps` uses AXI; `dibs ps --json` remains JSON. Byte-for-byte expectations
for list, ps, who, full lease records, truncated lists with counts, structured
help, budget summaries, and errors are in
[`testdata/output`](../../testdata/output). `go test ./...` also checks real PTY output,
child stdout/stderr passthrough, quiet IDs, shell exports, and setup/removal under
a temporary HOME. To update snapshots after an intentional format change, run
`UPDATE_GOLDEN=1 go test ./...` and review the fixture diff.
