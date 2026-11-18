# Leases and owner liveness

A lease records who owns a resource on this machine and when the resource expires. Automatic reclaim (`sweep` and job admission) happens only when the owner is dead or the lease has expired. A lease with live-owner protection is reclaimed automatically only when its owner is dead. An explicit `release --confirm` destroys a lease regardless of owner liveness or expiry. This page describes the lease lifecycle and the owner liveness checks that decide reclaim.

Source: `internal/lease/lease.go` (record, reclaim, extend, release, sweep), `internal/lease/manager.go` (the commands' ordering under the state lock), `internal/owner/reference.go` and `internal/owner/registry.go` (owner parsing and liveness), and the lease commands in `internal/cli/cli.go`. Tests: `internal/lease/lease_test.go`, `internal/owner/reference_test.go`, `internal/cli/cli_test.go`, and `TestFormalLeaseDomain` in `internal/lease/formal_test.go` (build tag `formal`, run with `just formal`). Formal model: `verification/Lease.tla`. Decisions: [ADR 0001](adr/0001-lease-lifecycle-and-ttl.md), [ADR 0002](adr/0002-reclaim-with-proof.md), [ADR 0003](adr/0003-owner-identity-and-liveness.md), [ADR 0008](adr/0008-live-owner-protection.md).

## Lease record

`newLease` in `internal/lease/lease.go` builds every lease.

- `ID` is 32 lowercase hex characters from 16 bytes of `crypto/rand`. The store validates the shape with the `leaseID` pattern `^[a-f0-9]{32}$`.
- `Machine` is the stable OS identity of the host, such as `linux:<machine-id>`. The lease belongs to that host for its whole life. See [State](state.md#machine-identity).
- `Created` and `Expires` are UTC times. At creation, `Expires` is `Created + TTL`. `extend` changes `Expires` to the extension time plus the new TTL, and `Created` stays the same, so `Created + TTL` is no longer the expiry after an extension.
- `TTL` must be positive. `newLease` and `extendLease` both reject `ttl <= 0`.
- `Description` is free-text metadata, stored as `resources`. It is not a CPU or memory limit.
- `Labels` is free-form `key=value` metadata from repeatable `--label` flags.
- `ResourceRef` names the adapter and the managed resource identity. A lease without a managed resource uses adapter `none`. See [Adapters](adapters.md).
- `Reservation` (JSON `job`) is set only for job reservations, which use adapter `process`. See [Jobs](jobs.md).
- `ProtectLiveOwner` (JSON `protect_live_owner`) is set by `--protect-live-owner` on `create` or `adopt`. See [Live-owner protection](#live-owner-protection). Job reservations never carry it.
- `IsScope` (JSON `is_scope`) marks a scope, set only by `dibs scope start`. See [Scopes](#scopes).
- `Handoff` (JSON `handoff`) records the last owner change: `from`, the previous owner, and `at`, when it changed. Only `dibs handoff --confirm` sets it. See [Handoff](#handoff).

The on-disk layout of these fields is in [State](state.md#format-version-2).

## Owner kinds

`newOwners` in `internal/cli/owners.go` registers three owner kinds. Each is a `Definition` in `internal/owner/registry.go` with a `Bind` function that parses and validates the owner when it is created, and a `Check` function that reports liveness later.

| Kind | ID | Recorded identity | Liveness source |
| --- | --- | --- | --- |
| `pid` | Positive PID that fits in a signed 32-bit integer | `Identity`, the `ps -o lstart=` start time | `kill(pid, 0)` plus `ps` |
| `session` | PID of the process that holds a session (not a Unix session ID) | Same as `pid` | Same as `pid` |
| `heartbeat` | File path, made absolute at parse time | `Stale`, the `--stale` duration (default `15m`) | File modification time |

The CLI picks the owner in this order: `--owner`, then `DIBS_OWNER` (or the legacy `BERTH_OWNER`), then `pid:<parent-pid>` (`run` in `internal/cli/cli.go`). `job` uses `pid:<own-pid>`. The SSH agent runs remote jobs as `session:<agent-pid>` (`runAgent` in `internal/cli/agent.go`).

`bind` in `internal/owner/reference.go` refuses an owner that is not alive at creation:

- A `pid` or `session` owner must be alive and inspectable.
- A `heartbeat` owner must be an existing, fresh, regular file.

## Liveness states

`Check` returns one of three `Liveness` values: `alive`, `dead`, or `unknown`. `unknown` means dibs cannot prove the owner either way. It is never treated as `dead`.

For `pid` and `session` owners, `ProcessIdentity` and the registry's `Check` decide:

| Observation | Result |
| --- | --- |
| Stored PID is invalid, out of range, or has no recorded identity | `unknown` |
| `kill(pid, 0)` fails with `ESRCH` | `dead` |
| `kill(pid, 0)` fails with any other error, such as `EPERM` | `unknown` |
| `ps` fails and the PID is now gone | `dead` |
| `ps` fails for another reason, or prints an unexpected shape | `unknown` |
| `ps` state starts with `Z` (zombie) | `dead` |
| Process is alive but its start time differs from the recorded identity (PID reuse) | `dead` |
| Process is alive with the recorded start time | `alive` |

`ps` runs with `LC_ALL=C` and `TZ=UTC` and a 3-second timeout. Start times have one-second precision, so PID reuse within the same second is not detected. A `ponytail:` comment in `ProcessIdentity` names that limit.

For `heartbeat` owners, `check` in `internal/owner/reference.go` decides:

| Observation | Result |
| --- | --- |
| `Stale <= 0`, or the path is not absolute | `unknown` |
| File does not exist | `dead` |
| `stat` fails for another reason, the path is not a regular file, or its modification time is in the future | `unknown` |
| File age is at least `Stale` | `dead` |
| Otherwise | `alive` |

Tests: `TestOwnerLiveness`, `TestOutOfRangePIDIsUnknownNotDead`, `TestHeartbeatIgnoresProcessIdentity`.

## Reclaim rule

`reclaimable` in `internal/lease/lease.go` is the single reclaim decision. `sweep`, `list`, and job admission all call it.

1. If the lease's `Machine` is not this host, the lease is not reclaimable. The owner check does not run, and liveness reports `unknown`.
2. If the lease has live-owner protection, it is reclaimable only when the owner is `dead`.
3. Otherwise the lease is reclaimable when the owner is `dead` **or** `now >= Expires`.

These cases follow from the rule:

| Owner | Expired | Reclaimable |
| --- | --- | --- |
| `alive` | no | no |
| `unknown` | no | no |
| `dead` | either | yes |
| `alive` or `unknown` | yes | yes |
| any, on a foreign machine | either | no |

With live-owner protection:

| Owner | Expired | Reclaimable |
| --- | --- | --- |
| `alive` or `unknown` | either | no |
| `dead` | either | yes |
| any, on a foreign machine | either | no |

Tests: `TestReclaimAndExtend`, `TestSweepDecisions`, `TestCLIExpiredLiveOwnerAndFailure`, `TestProtectedReclaimAndRenew`, `TestProtectedSweepSurvivesSupervisorOutage`.

## Lifecycle commands

### Create

`Manager.Create` in `internal/lease/manager.go` creates a lease in this order:

1. `open` takes the host-wide state lock (see [State](state.md#locking)), then reads the clock. The clock is read after the lock, because acquiring the lock can wait behind a slow adapter.
2. The owner is checked again. A stale or unavailable owner fails the create.
3. `newLease` builds the record, and the adapter's `Prepare` captures the cleanup scope. See [Adapters](adapters.md).
4. The lease is saved to `state.json`.
5. The adapter's `Create` runs and returns the identity to record. When that identity differs from the prepared one (for example, a treehouse slot path and lease token), the manager validates it and saves it again, even when `Create` then reports an error.

If `Create` fails, the lease stays in state so that `release` or `sweep` can clean up a partial resource. The error names the retained lease ID: `creation incomplete; lease <id> retained for cleanup`. Test: `TestCLICreateFailureKeepsDurableCleanupIntent`.

With the consumer contract on (`DIBS_CONSUMER_CONTRACT=1` or `"consumer_contract": true`), two flags extend `create`. With `--key`, `keyed` runs first, right after the lock: a lease on this machine with the same key, owner and pinned owner identity, protection, adapter, mode, and handle is returned unchanged, and a different request with that key is refused. With `--pending`, `recordPending` replaces steps 3 to 5: it records the scope and optional pinned handle, refuses a pinned handle that already exists or that another lease claims, and saves the lease without running the adapter. The caller builds the resource and binds it with `adopt --pending`. See [Consumer contract](consumer-contract.md#crash-safe-acquire). Tests: `TestIdempotencyKeys`, `TestPendingPinnedHandleLifecycle`.

### Extend

`extendLease` sets `Expires = now + ttl`. It refuses when any of these is true:

- `ttl` is not positive.
- The lease belongs to another machine.
- The lease is a job reservation (`Reservation != nil`). Job deadlines never move.
- The lease is reclaimable, or its owner is not `alive`.

An expired lease cannot come back. Create a new one instead. Test: `TestReclaimAndExtend`.

A protected lease follows [Live-owner protection](#live-owner-protection) instead of the last rule: after expiry it reattaches only while its adapter's `Exists` reports the resource present. A terminal lease follows the same rule even without protection, because reclaim treats it as owner-bound; its `Exists` is the terminal adapter's `observe`, so it reattaches only while the pinned terminal is still there, and a missing or unreadable terminal refuses with `resource_absent` or `resource_unknown`. Test: `TestExpiredTerminalRenewalProvesTheTerminal`. `Manager.Extend` reports `renewed` or `reattached` as the result's `status`.

A non-positive `--ttl` is a usage error (exit 2) before any state access. With the consumer contract on, every other refusal exits 3 with a stable reason, adding `not_found`, `wrong_machine`, `job_deadline`, and `expired` to the protected-lease reasons; when an unprotected lease is expired and its owner is dead or unknown, the owner reason wins. Without it, those refusals keep exit 1. Tests: `TestRenewReleaseContractModes`, `TestCLIUnprotectedLateRenewUnchanged`.

### Release

`dibs release <id>` is a preview unless `--confirm` is set. With `--confirm`, `Manager.Release` calls `releaseLease`, which:

1. Returns success with no change when the lease is absent.
2. Refuses a lease from another machine.
3. Refuses a lease whose adapter this binary does not know, and keeps the lease. The `process` adapter is handled by the lease package itself, not the adapter registry.
4. For a pending lease, `settlePending` looks for its match first. A match already under another lease refuses with `already_leased`, and several matches refuse with `ambiguous`; both keep the lease. One unclaimed match is bound, then destroyed below. No match means nothing was built, and the lease is forgotten without an adapter call. Failed discovery keeps the lease. Tests: `TestPendingCleanupAfterCrash`, `TestPendingCleanupRetainsConflictingResource`, `TestReconcileAmbiguousConflictUnknown`.
5. Calls the adapter's `Destroy`, or the process group's `Destroy`. On error, keeps the lease.
6. Deletes the lease and saves (`forget`). If the save fails, restores the lease in memory and returns the error.

An adapter that sets `Definition.CleanupOutsideLock` (today only `treehouse-worktree`) takes a different path, so a slow cleanup does not stall other commands. Under the lock, `Release` reads the lease and checks its machine. Then it unlocks and runs `Destroy`. Before unlocking, it records `releasing` in the lease. `extend` refuses a marked lease with `cleanup_in_progress`, closing the renewal race while cleanup is running. A failed cleanup keeps the marker so release or sweep can retry it; a refusal for unlanded work or running processes touched nothing, so it restores the marker the lease had before that attempt (`TestRefusedCleanupRestoresTheRenewalMarker`). `Reclaim` and `Sweep` take the same path, never with `DiscardUnlanded`. Then release locks again and deletes the record only if the recorded `ResourceRef` is unchanged; a changed identity keeps the lease and returns an error. `--discard-unlanded` sets `Target.DiscardUnlanded` for this `Destroy` only. Tests: `TestReleaseCleansUpOutsideLockAndForgetsAfter`, `TestReleaseOutsideLockRetainsOnFailureOrChange`.

`release --confirm` does not check owner liveness or expiry, with or without live-owner protection. It is an explicit operator action on a specific lease. Tests: `TestCLILeaseLifecycle`, `TestCLIReleaseFailureDoesNotReportReleased`.

With the consumer contract on, `release` reports `preview`, `released`, or `already_done`, refuses a foreign lease with `wrong_machine` (exit 3) before any preview, and refuses an ambiguous or conflicting pending lease with exit 3. Without it, the status strings and exit codes above are unchanged. Test: `TestRenewReleaseContractModes`.

### Sweep

`sweep` in `internal/lease/lease.go` walks leases in sorted ID order, with scopes last so it can release dead members first. It records `unknown` owners, collects reclaimable leases as `eligible`, and, with `--confirm`, calls `releaseLease` on each eligible lease. A failed release goes into `Failures` and keeps its lease. Eligibility is still decided under the lock. Eligible leases whose adapter cleans up outside the lock are marked `releasing` and saved before `sweep` unlocks, then released one at a time through the same path as `Release`, and never with `DiscardUnlanded`. A failed cleanup retains the marker for a later retry. `dibs sweep` exits 1 when any failure occurred. Test: `TestSweepReclaimsOutsideLockWithoutDiscarding`. A second sweep after success is a no-op. Test: `TestSweepPreviewRetryAndIdempotence`.

### Reclaim and explain

`Manager.Reclaim` in `internal/lease/pending.go` is a targeted reclaim that is stricter than both `release` and the reclaim rule above, with or without live-owner protection. It acts only when the owner check returns `dead` on this machine. An `alive` owner is refused even after expiry, and `unknown` liveness is refused. It previews without `--confirm`, then calls `releaseLease`, so a job lease whose leader is already gone and reaped is released without verifying its descendants, as in invariant 4. An absent lease is success. `Manager.Explain` reports the reclaim rule's answer, the targeted reclaim's answer, and a readable reason, without acting. Both need the consumer contract. Tests: `TestReclaimRequiresProvenDeath`, `TestExplainUnexpired`.

dibs has no daemon. Reclaim happens only when someone runs `dibs sweep --confirm`, `dibs release --confirm`, `dibs reclaim --confirm`, or `dibs job` (whose admission reclaims eligible job leases), or when an opt-in [lease watcher](#lease-watcher) sees its owner exit. The help text tells users to run sweep from a host timer.

## Live-owner protection

`--protect-live-owner` on `create` or `adopt` stores `protect_live_owner: true`. The flag needs the `live_owner_protection` user config opt-in or `DIBS_LIVE_OWNER_PROTECTION=1`, and fails with code 2 without it. Without the flag, nothing on this page changes. [ADR 0008](adr/0008-live-owner-protection.md) records the decision.

`reclaimable` reclaims a protected lease only on proven owner death. `extendLease` checks a protected lease in this order:

| State | Result | Exit |
| --- | --- | --- |
| Owner `dead` | `RenewRefusal` reason `owner_dead` | 3 |
| Owner `unknown` | reason `owner_unknown` | 3 |
| Owner `alive`, unexpired | `renewed`, `Expires = now + ttl` | 0 |
| Owner `alive`, expired, adapter `Exists` reports present | `reattached`, `Expires = now + ttl` | 0 |
| Owner `alive`, expired, `Exists` reports absent | reason `resource_absent` | 3 |
| Owner `alive`, expired, `Exists` fails or is missing | reason `resource_unknown` | 3 |

The ttl, machine, and job checks come first and keep their exit 1 errors. A refusal leaves the record unchanged. A renew never creates a lease, so a late renew cannot give one resource two owners. `Exists` runs only for a late renew, under the state lock. See [Adapters](adapters.md#contract).

Protection holds through a supervisor outage only when the lease owner is the worker's own liveness source. If the owner is the renewing supervisor's PID, or the supervisor touches the heartbeat file, a supervisor outage reads as owner death and the lease is reclaimed.

Tests: `TestProtectedReclaimAndRenew`, `TestProtectedAdoptReattachesOnlyToPinnedEndpoint`, `TestCLIProtectedRenewRefusalsByOwner`, `TestCLIProtectedLateRenewChecksPinnedResource`, `TestCLIUnprotectedLateRenewUnchanged`, `TestLiveOwnerProtectionOptIn`.

## Scopes

A scope groups and owns the leases of one unit of work. It requires `"scopes": true` in user config or `DIBS_SCOPES=1`; otherwise scope commands and the `scope:` owner kind are off. `dibs scope start --key K` gets or creates a scope keyed by the caller's task or session id. Create or adopt members with `--owner scope:<id>`. `dibs scope end <id> --confirm` releases the members newest first. A scope remains recorded while any member names it as owner. The code is `internal/lease/scope.go` and `internal/cli/scope.go`.

- **A scope is a lease with no external resource.** `StartScope` records an `is_scope` lease with adapter `none`, its own owner and TTL, and the description `scope` followed by its key. `dibs extend <id>` renews it like any lease. A scope cannot be owned by another scope.
- **`scope:<id>` is an owner kind.** `--owner scope:<id>` on `create` or `adopt` makes the lease a member of that scope. `ScopeOwner` only parses the ID. `Manager.liveness` resolves it against the ledger under the lock: `alive` while the scope lease exists and its owner is alive, regardless of TTL when the scope has an owner-bound policy; otherwise it follows expiry. It is `dead` once the scope is absent, not a scope, or itself reclaimable, and `unknown` while its own owner is unknown. Every lease command that checks owners uses this view, so a member follows the reclaim rule above like any other lease. A member cannot join a scope that is not alive; that refusal has reason `scope_ended`.
- **The key is the caller's own id.** `dibs scope start --key K` is get-or-create through `keyed`: the same key and owner return the same scope while it is not reclaimable, a different owner or a non-scope lease with that key is `key_conflict`, and a key whose scope has ended is `scope_ended`. Once the ended scope is forgotten, the key is free again.
- **Ending is explicit.** `EndScope` previews the members, newest first. With `--confirm` it sets the scope's expiry to now and saves, which makes every member's owner dead so nothing new can join. Then it releases each member through `Manager.Release`, the ordinary release with proof, and forgets the scope once no member remains. A failed or refused release keeps that member and the scope, and a rerun releases only what remains. An absent scope is `already_done`.
- **Scopes and their members take policy rows.** With [lease policies](guides/owners.md#lease-policies) on, `StartScope` records the `scope` row on the scope: owner-bound, so the scope, and with it its members' owner, stays alive past its 30 minute TTL while the scope's own owner lives. `EndScope` drops that policy as it expires the scope, so ending still makes every member's owner dead. A member's policy comes from the `<kind>/scope` row when one exists. Built-in rows give a scope's shells, terminals, and worktrees a 30 minute renewal cadence and a 10 minute grace. Tests: `TestOwnerBoundScope`, `TestScopeStartRecordsItsPolicy`, `TestPolicyRowForScopeOwner`.
- **A scope is forgotten only when it has no members.** `holdScope` turns a scope's reclaim verdict into the hold `scope_members` while any lease names it as owner, in `sweep`, `reclaim` and `explain`, and `release` refuses such a scope with `scope_members`.

Tests: `TestScopeStartEndAndForget`, `TestEndScopeReleasesNewestFirst`, `TestScopeOwnerLiveness`, `TestSweepReleasesMembersThenTheirScope`, `TestSweepHoldsScopeWhileAMemberRemains`, `TestScopeCommands`.

## Lease watcher

The lease watcher is off by default. `"lease_watcher": true` in the user config or `DIBS_LEASE_WATCHER=1` turns it on; the variable overrides the config either way. With the setting absent, nothing on this page changes. [Issue 55](https://github.com/Dematom1/dibs/issues/55) records the decision.

When it is on, `create` and `adopt` start one detached `dibs __watch <id>` for a lease whose owner is `pid:` or `session:` (`Watchable` in `internal/lease/watch.go`). A job reservation gets none, because `dibs job` already waits on its child. A remote `create` starts the watcher on the remote machine, under that machine's config. A failed start is not an error; the next sweep still reclaims the lease. `__watch` is internal and not part of the CLI contract.

`Manager.Watch` lives exactly as long as its lease:

- It holds an exclusive `flock` on `<dir>/watch/<lease-id>.lock`. The lock file remains after exit; only its `flock` indicates a live watcher. A second watcher for the same lease exits at once, and the kernel drops the lock when the watcher dies.
- It attaches to the owner's exit first (kqueue `EVFILT_PROC` with `NOTE_EXIT` on macOS, `pidfd_open` and `poll` on Linux 5.3 or newer), then checks the owner. Attaching first means an exit right after the check still wakes it. When it cannot attach, it falls back to waking once a minute.
- It wakes on the owner's exit or after one minute (`watchInterval`), and reads the ledger again. It exits when the lease is gone, so a released lease ends its watcher within a minute. A wake that ends early without proving death, such as an exit event for a zombie whose `ps` read fails, makes the next wait a plain one-minute sleep, so an inconclusive event never spins.
- An event only says when to look. When the owner check returns `dead` (the process is gone, a zombie, or its start identity changed), it runs `Manager.Sweep` with `Confirm`, the lease's `ID`, and `OwnerDead`. That is the confirmed sweep for this one lease: the [verdict](consumer-contract.md#reclaim-and-sweep) still applies every hold, so a `report` policy stays shadow-only (`report_only`), a grace period starts its clock, and a busy resource is kept. `unknown` liveness never reclaims, and expiry never wakes the watcher.
- It never retries. After one reclaim attempt it exits, and a retained lease waits for the next `dibs sweep --confirm`. Keep the host timer: it is the backstop for a dead watcher, a reboot, a grace period, and every owner the watcher cannot follow.
- It holds no state beyond its lock file, and killing it changes no guarantee.

Tests: `TestWatcherReclaimsOnOwnerExit`, `TestWatcherHonorsReportPolicy`, `TestWatcherIgnoresExpiryAndExitsOnRelease`, `TestWatcherIsSingle`, `TestWatcherThrottlesInconclusiveExit`, `TestLeaseWatcherOptIn`.

## Handoff

`dibs handoff <id> --owner KIND:ID` hands a lease to a new owner. The case it serves is a worktree that passes from its task scope to its pull request (`--owner pr:OWNER/REPO#N`), so the worktree outlives the task while the pull request is open. It needs the `handoff` user config opt-in or `DIBS_HANDOFF=1`; without it, `dibs handoff` exits 2 naming the setting, and nothing else on this page changes. The code is `internal/lease/handoff.go` and `internal/cli/handoff.go`.

- **Preview first.** Without `--confirm`, `Manager.Handoff` makes every check and reports the decision, and changes nothing. With `--confirm` it saves the new owner under the state lock.
- **The owner and policy row change.** The resource, `Expires`, and protection stay as they were. With lease policies enabled, the lease records the row for its new owner kind; with policies off, its recorded policy stays unchanged. A recorded owner-death stamp is cleared, because it described the old owner. Labels an adapter wrote on the resource at create are not rewritten; the ledger is the authority.
- **The new owner must be proven alive.** `--owner` binds as on `create`, so a malformed selector, a process that is gone, or a closed or merged pull request exits 2. Under the state lock the bound owner is checked again through `Manager.liveness`, so `scope:<id>` resolves against the ledger. An owner that is dead or unknown there, such as an ended scope, is refused with reason `owner_not_alive`. The old owner's liveness does not matter, so a lease whose owner died can still be handed to a live one.
- **A lease handed out of a scope leaves it.** Scope membership is the `scope:<id>` owner, so after a handoff to any other owner `dibs scope end` no longer lists or releases the lease, and the scope can be forgotten while the lease lives on.
- **Retries converge.** A lease that already has the requested owner reports `already_done` and is not written again.
- **What is refused.** Besides `owner_not_alive`: `not_found`, `wrong_machine`, `cleanup_in_progress` while a release is under way, `job_lease` for a job reservation, `scope_nested` for a scope handed to a scope, and `expired` for a time-bound lease past its expiry, which sweep would reclaim whatever its owner. Renew such a lease with `dibs extend` first.
- **`explain` shows it.** `Handoff` is recorded on the lease, and `dibs explain <id>` prints it as `handoff` with `from` and `at`. Only the last handoff is kept.

A keyed lease keeps its key. A retry of the original `create --key` with the old owner is then a different request and returns `key_conflict`. A scope can be handed to a new owner too, so a restarted caller with a new process can keep it.

Tests: `TestHandoffMovesALeaseOutOfItsScope`, `TestHandoffRefusalsChangeNothing`, `TestHandoffRecordNeedsVersionEight`, `TestHandoffCommand`.

## Renewal cadence

The `cadence` constant in `internal/cli/help.go` is printed in `create` and `extend` help:

- Touch heartbeat files at most every `stale / 3`.
- Extend a lease at the latest when one third of its TTL remains.

The cadence is a recommendation. Missing it does not by itself make a lease reclaimable. A live owner that misses it can still extend an unexpired lease. If the lease then expires, or its owner dies, a later `sweep --confirm` reclaims it. `TestCLIHomeHelpVersionAndTOON` asserts the help text.

## Invariants

Reviewers check every change to `internal/lease/lease.go`, `internal/lease/manager.go`, `internal/owner/`, and the lease commands in `internal/cli/cli.go` against these rules.

1. **Reclaim follows the recorded lease rule.** A time-bound lease can be reclaimed at expiry even with a live or unknown owner; an owner-bound lease requires proven owner death. Shell leases remain held for live or unknown owners, unless a hybrid shell policy sets `reclaim_live_owner: true`, which lets its idle or maximum-lifetime trigger reclaim a proven-live owner. Busy or unobservable resources are never reclaimed. See [lease policies](guides/owners.md#lease-policies) and [consumer contract](consumer-contract.md#reclaim-and-sweep). Tests: `TestReclaimAndExtend`, `TestSweepDecisions`, `TestFormalLeaseDomain`, `TestProtectedReclaimAndRenew`.
2. **`unknown` is never `dead`.** Any error that does not prove absence returns `unknown`. Unknown liveness alone never permits reclaim; the recorded policy determines whether expiry can still reclaim. Tests: `TestOwnerLiveness`, `TestOutOfRangePIDIsUnknownNotDead`.
3. **A host acts only on its own leases.** `reclaimable`, `extendLease`, and `releaseLease` compare `Machine` with this host before anything else. Expired foreign leases stay. Test: `TestReclaimAndExtend`.
4. **A record is removed only after verify-gone.** `releaseLease` deletes the lease only after `Destroy` returns nil, and every built-in adapter's `Destroy` verifies absence before returning nil. The one exception is a job reservation whose group leader is already gone and reaped: `processResource.Destroy` then succeeds without verifying that descendants are gone. See [Jobs](jobs.md#process-group-cleanup) and [ADR 0002](adr/0002-reclaim-with-proof.md#existing-process-exception). See [Adapters](adapters.md#invariants). Test: `TestSweepPreviewRetryAndIdempotence`.
5. **Cleanup intent is durable before the resource exists.** `Manager.Create` saves the lease before the adapter's `Create` runs. Test: `TestCLICreateFailureKeepsDurableCleanupIntent`.
6. **Preview by default.** `release` and `sweep` change nothing without `--confirm`. Test: `TestSweepPreviewRetryAndIdempotence`.
7. **Expiry is checked after the lock.** `Manager.open` reads the clock after `openStore` has acquired the lock.
8. **PID owners pin start identity.** A reused PID reads as `dead`, never as the original owner. The registry's `Check` compares the recorded identity with the live one. Test: `TestOwnerLiveness`.
9. **A late renew never recreates.** A protected lease reattaches only to the same lease ID and a present resource. Every refusal leaves the record unchanged. Test: `TestProtectedReclaimAndRenew`.
10. **Job deadlines never move.** `extendLease` refuses reservations. Test: `TestJobsRealProcesses` covers the job lifecycle. See [Jobs](jobs.md#invariants).
11. **A scope outlives its members.** No path forgets a scope while a lease names it as owner, and ending a scope releases only its members. Tests: `TestSweepHoldsScopeWhileAMemberRemains`, `TestScopeStartEndAndForget`.

## Owner extensions

The owner registry supports native owner kinds and the opt-in `exec` owner, which runs a caller-supplied liveness command. See [exec owners](guides/owners.md#exec-owners) for its user-facing contract. The broader extension direction remains recorded in [ADR 0005](adr/0005-adapter-and-owner-extension-contracts.md).
