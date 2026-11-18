# Owners

## Owners and renewal

`--owner` overrides `DIBS_OWNER`. Without either, dibs uses the parent PID,
usually the invoking shell. A spawner can export `DIBS_OWNER` for all of its
workers. The supported owner kinds are:

- `pid:<pid>` follows a local process and records its start time to detect PID
  reuse. Process start identity comes from `ps` with one-second precision.
- `session:<holder-pid>` follows the local process holding a session, with the
  same start-time check. This is not a Unix session ID. The SSH agent uses its
  own session-holding process as the owner without querying a client PID.
- `heartbeat:<file>` follows an existing regular file's modification time.
  `--stale` defaults to `15m`. A missing file or a stale heartbeat means dead;
  an unreadable file or a timestamp in the future means unknown. Create the
  file before creating the lease.
- `pr:OWNER/REPO#N`, `pr:HOST/OWNER/REPO#N`, or
  `pr:https://HOST/OWNER/REPO/pull/N` follows a GitHub pull request. It is off
  until you opt in; see below.
- `exec:<command>` follows a liveness command that any tool supplies. It is off
  until you opt in; see below.

### Exec owners

The `exec` owner is off by default. Turn it on per machine with
`"exec_owner": true` in the user config, or with `DIBS_EXEC_OWNER=1`; the
variable overrides the config either way. While it is off, `--owner exec:...`
fails with code 2, and an already recorded `exec` lease reads as unknown. Only
expiry then reclaims an unprotected time-bound lease; shell, owner-bound, hybrid,
and `--protect-live-owner` leases are kept while their owner is unknown.

Dibs runs the command with `/bin/sh -c` from `/`, with stdin from `/dev/null`,
output discarded, and a 10-second timeout, in its own process group so a timeout
stops everything it started. It runs once per owner check, so a list, sweep, or
explain runs every `exec` lease's command once.

| Probe result | Liveness |
| --- | --- |
| Exit 0 | alive |
| Exit 1 through 125 | dead |
| Timeout, a signal, exit 126 (cannot execute) or 127 (not found), or a failure to start | unknown |

`create` and `adopt` require the probe to report alive. Use absolute paths, and
keep secrets out of the command: it is stored in the lease and shown as the owner.

### Pull request owners

The `pr` owner is off by default. Turn it on per machine with `"pr_owner": true`
in the user config, or with `DIBS_PR_OWNER=1`. `DIBS_PR_OWNER` overrides the
config either way, so `DIBS_PR_OWNER=0` turns a config opt-in off. Project files
cannot enable it. While it is off, `--owner pr:...` fails with code 2, and an
already recorded `pr` lease reads as unknown. Only expiry then reclaims an
unprotected time-bound lease; shell, owner-bound, hybrid, and
`--protect-live-owner` leases are kept while their owner is unknown.

Dibs reads the pull request with one `gh api` GET per owner check, using your
existing `gh` authentication for that host. It never writes to GitHub and never
passes or logs a token. Each check has a 10-second timeout and no retries. A
sweep, list, or extend costs about one API call per `pr` lease on this machine.
GitHub Enterprise hosts work through `gh api --hostname`.

- Open, including draft, means alive. Closed or merged means dead.
- Any API error, timeout, rate limit, authentication failure, missing `gh`,
  malformed response, or 404 means unknown. GitHub hides private resources you
  cannot read behind a 404, so a 404 is not proof that the PR is gone.
- `create` requires an open PR and pins the host, the PR's `node_id`, and its
  base repository's `node_id`. A later response with a different identity, such
  as a new PR at the same path, means unknown. A renamed or transferred
  repository keeps working because GitHub redirects the old path.

TTL and sweep stay the backstop. Expiry still reclaims an open PR's lease unless
a lease policy binds that lease to its owner (see the policy table below), so
extend it on the usual cadence. Dibs sees only the state at each check. It
misses a close and reopen between two checks, and reopening a PR does not bring
back a lease that sweep already reclaimed.

```sh
export DIBS_PR_OWNER=1
dibs create --adapter compose --file compose.yaml \
  --owner pr:https://github.com/OWNER/REPO/pull/123 --ttl 8h
dibs sweep --confirm   # after the PR closes or merges
```

Touch a heartbeat file at most every staleness/3. Extend a lease at the latest
when one third of its TTL remains. A lease is reclaimable when its owner is
dead **or** when it has expired. A live owner that misses its TTL renewal loses
an unprotected time-bound lease at expiry; shell, terminal, and protected leases stay held.
A heartbeat older than its staleness makes the owner dead, so its shell, terminal,
and protected leases become reclaimable under the reclaim rule. Unknown liveness alone
never permits reclaim for shell, terminal, or protected leases; other time-bound
leases may still be reclaimed at expiry. `extend` refuses expired leases and owners that are dead or unknown,
except that a protected or terminal lease with a live owner reattaches after expiry while
its resource is present (see below).

```sh
 touch /tmp/my-lab-heartbeat
 dibs create --owner heartbeat:/tmp/my-lab-heartbeat --stale 15m --ttl 1h
 # The owner touches the file at least every 5m, and extends before 20m remain.
 dibs extend <id> --ttl 1h
```

### Protect a live owner's lease

A supervisor that renews leases for long-lived workers can opt a lease out of
the expiry backstop. Protection is off by default. Turn it on per machine with
`"live_owner_protection": true` in the user config, or with
`DIBS_LIVE_OWNER_PROTECTION=1`; the variable overrides the config either way.
Project files cannot enable it. While it is off, `--protect-live-owner` fails
with code 2. Turning it off later does not unprotect leases already recorded
with it.

Pass `--protect-live-owner` to `create` or `adopt`. Then sweep reclaims the lease only when its owner is proven dead. An owner that is
alive or unknown keeps the lease, even after expiry, so a supervisor outage
never costs a running worker its resources. Leases without the flag behave
exactly as above.

```sh
 export DIBS_LIVE_OWNER_PROTECTION=1
 id=$(dibs create --adapter kind --owner "pid:$worker_pid" --ttl 30m \
   --protect-live-owner --quiet)
 dibs extend "$id" --ttl 30m       # status: renewed, or reattached after expiry
 dibs release "$id" --confirm      # explicit release needs nothing extra
```

Use the worker's own process, session, or heartbeat as the owner. If the
supervisor's PID is the owner, or the supervisor touches the heartbeat, its
outage reads as owner death.

`extend` on a protected lease with a live owner succeeds before expiry
(`status: renewed`). After expiry it reattaches the same lease (`status:
reattached`) only when the adapter proves the pinned resource is still present.
For kind, k3d, and Compose, the cluster or project must still be visible in the
pinned Docker context, at the pinned endpoint for adopted resources. For
`treehouse-worktree`, the pool must still carry the lease's treehouse token. A renew
never creates a lease. A refusal for one of these reasons exits 3, leaves the
lease unchanged, and names the `reason` in AXI and JSON output; any other
refusal, such as a lease on another machine, exits 1 as for any lease:

| `reason` | Meaning | Next step |
| --- | --- | --- |
| `owner_dead` | The owner is proven dead | `dibs release <id> --confirm` or `dibs sweep --confirm` |
| `owner_unknown` | The owner cannot be verified | Retry later; the lease is kept and not reclaimable |
| `resource_absent` | The expired lease's resource is gone | `dibs release <id> --confirm`, then lease a new resource |
| `resource_unknown` | The resource could not be checked | Retry once the resource tool is reachable |

A protected lease whose owner stays unknown is never reclaimed automatically.
It stays in `dibs ps` until someone releases it. Job reservations cannot be
protected.

Durations use Go syntax such as `30m`, `2h`, or `1h30m`. Every lease has a positive
TTL. `create` defaults to `8h`; `adopt` requires an explicit `--ttl`.
`extend --ttl` sets the expiry to now plus that duration.

### Lease policies

Leases differ: a shell should live while its task lives, and a test lab should
live for a few renewable hours with a hard cap. The policy table decides each
new lease's lifetime by resource kind, optionally narrowed by owner kind. It is
off by default. Turn it on per machine with `"lease_policies": true` in the user
config, or with `DIBS_LEASE_POLICIES=1`; the variable overrides the config either
way. While it is off, `--idle` and `--max-lifetime` fail with code 2 and name the
setting, and every lease follows its TTL as described above.

| Field | Meaning |
| --- | --- |
| `binding` | `time`: the lease ends at expiry or owner death. `owner`: it lives exactly while its owner is alive; expiry never reclaims it. `hybrid`: owner-bound, and it also ends after `idle` or `max_lifetime` |
| `ttl` | TTL when `--ttl` is omitted, including for `adopt`; default `8h` |
| `renew` | Advised renewal cadence; `explain` shows `renew_by` |
| `max_lifetime` | Hard cap from creation; live-owner reclamation requires `reclaim_live_owner: true` on a hybrid row; `extend` never passes it |
| `grace` | How long a reclaim condition must hold before dibs acts; for owner death it counts from the first confirmed sweep that saw the owner dead; `reclaim` never starts it |
| `idle` | Hybrid only: reclaim after this idle duration. A live owner is reclaimed only with `reclaim_live_owner: true`; unknown owners are never reclaimed. Shell idle means no foreground job, measured from terminal input/output; a shell without a terminal never goes idle |
| `reclaim_live_owner` | Hybrid only, default `false`: allow idle or max_lifetime to reclaim a shell with a proven-live owner; unknown owners and TTL expiry remain protected |
| `action` | `destroy` (default) reclaims; `report` lists the lease under `held` in `sweep` but never reclaims it automatically. Explicit `release` and `reclaim` still act |

Built-in rows apply when the table is on:

| Row | Binding | TTL | Renew | Max lifetime | Idle | Grace | Reclaim live owner |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `shell` | `hybrid` | `8h` | - | - | `30m` | - | `false` |
| `shell/scope` | `hybrid` | `30m` | - | - | `30m` | `10m` | `false` |
| `tmux`, `herdr`, `rex`, `treehouse-subshell` | `owner` | `8h` | - | - | - | - | - |
| `tmux/scope`, `herdr/scope`, `rex/scope`, `treehouse-subshell/scope` | `owner` | `30m` | - | - | - | `10m` | - |
| `treehouse-worktree` | `owner` | `8h` | - | - | - | - | - |
| `treehouse-worktree/scope` | `owner` | `30m` | - | - | - | `10m` | - |
| `kind`, `k3d`, `compose` | `time` | `4h` | `2h` | `24h` | - | - | - |
| `none` | `time` | `8h` | - | - | - | - | - |
| `scope` | `owner` | `30m` | - | - | - | - | - |

The `scope` row is the scope lease itself: `dibs scope start` records it, so a
scope lives while its owner does. An explicit `--ttl` may only shorten its
30-minute TTL. This row has no `max_lifetime` and cannot be narrowed by owner
kind. `dibs scope end --confirm` drops the row so the ended scope no longer
holds its members. The `/scope` rows apply to the members of a
[scope](../leases.md#scopes): their 30-minute TTL and 10-minute grace are
recorded on each lease. A worktree held by a pull request owner uses the
`treehouse-worktree` row, owner-bound with an 8 hour TTL. Labs keep their row
whatever owns them.

Job deadlines come from job classes, and a `process` row is refused. A row you
configure replaces the built-in row with the same key; omitted fields are off.
Key a row by resource kind, or by `<kind>/<owner kind>`, which takes precedence:

```json
{
  "lease_policies": true,
  "policies": {
    "shell": {"binding": "hybrid", "idle": "45m", "grace": "2m", "reclaim_live_owner": true},
    "kind/pr": {"binding": "owner"},
    "compose": {"binding": "time", "ttl": "2h", "max_lifetime": "12h", "action": "report"}
  }
}
```

In the shell row, `reclaim_live_owner: true` is an explicit opt-in that overrides
retaining shells with a live owner; omit it to keep the default protection.

An `owner` binding takes no `idle` or `max_lifetime`, a `time` binding takes no
`idle`, and a `hybrid` binding needs one of them. Only adapters that report
activity accept `idle`; today that is `shell`. Unknown fields and invalid rows
fail `create` and `adopt` with code 2.

`--ttl` on `create` or `adopt` may only shorten the selected row's TTL and
must fit within `max_lifetime` when the row sets one. `--idle D` and
`--max-lifetime D` may only tighten the row. Each lease records its effective
policy when it is created, so later config edits, or turning the setting off,
never loosen or tighten an existing lease. `--protect-live-owner` outranks a
policy: only proven owner death reclaims a protected lease.

Shell leases are owner-bound even without a recorded policy: TTL expiry never reclaims them while the owner is alive or unknown. The default shell hybrid idle timeout is inert for a live owner; set `reclaim_live_owner: true` explicitly to permit idle or maximum-lifetime reclamation of a live owner. Unknown owners and busy resources are always retained. `dibs explain`
shows the governing policy (`source: lease`, or `legacy` for a lease acquired
without one), the condition that fired, when the next condition ends
(`reclaim_at`, `remaining`), and why the lease is still held (`held_because`).
