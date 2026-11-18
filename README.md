# dibs

Owner-and-expiry leases for local stacks, clusters, shells, and jobs. One Go
binary, no daemon. By default, `dibs sweep --confirm` reclaims a lease once its
owner has died or its expiry has passed.

## Demo

![A worker leases a resource, its owner dies, sweep reclaims the lease, and a shell lease is released](demo/demo.gif)

Every command and its output in this recording are real, from the released
v0.2.4 binary with an empty state directory. The tape only replays the keystrokes.
[demo/record.sh](demo/record.sh) downloads that release, checks it against
`checksums.txt`, and renders [demo/demo.gif](demo/demo.gif) from
[demo/demo.tape](demo/demo.tape) with [VHS](https://github.com/charmbracelet/vhs).
Run `demo/record.sh` to reproduce the GIF; it needs `vhs`, `jq`, and `shasum` on `PATH`.

## One-minute start

Install with Go 1.26 or later: `go install github.com/Dematom1/dibs@latest`.
Nix, release archives, and checkout builds are in [docs/guides/install.md](docs/guides/install.md).

```sh
export DIBS_STATE="$(mktemp -d)"       # a throwaway state dir, so sweep cannot touch your own leases
sleep 600 & WORKER=$!                  # a process that will own the lease
dibs create --adapter none --owner pid:$WORKER --ttl 2h --resources 'demo worker' --label task=demo
dibs ps --json | jq '.leases[] | {id, owner, liveness, expires}'
kill $WORKER                           # the owner dies; the lease stays recorded
dibs sweep                             # preview: the dead-owner lease is eligible
dibs sweep --confirm                   # reclaim it
LEASE=$(dibs create --adapter none --owner pid:$$ --ttl 1h --resources 'demo shell' --quiet)
dibs release $LEASE                    # preview
dibs release $LEASE --confirm          # release it
```

These are the commands in the demo, which also starts from an empty state
directory. `dibs ps` prints a wide table on a terminal, so the demo filters its
JSON with `jq`.

## What it leases

| Resource | Adapter | What dibs does | Needs |
| --- | --- | --- | --- |
| Nothing external (metadata only) | `none`, the default for `create` | Records the owner, expiry, and labels. Release and sweep forget the record and delete nothing. | Nothing |
| kind cluster | `kind` | Creates a uniquely named cluster with a private kubeconfig, and deletes it on release. Your kubeconfig is not touched. | `kind`, `docker`, a running Docker engine |
| k3d cluster | `k3d` | Creates a cluster with a private kubeconfig, and deletes it on release. Your kubeconfig is not touched. | `k3d`, `docker`, a running Docker engine |
| Docker Compose project | `compose` | Runs a resolved snapshot of your Compose file. Release runs `down --volumes`, which deletes the project's volumes. | `docker compose`, a running Docker engine |
| Existing kind, k3d, or Compose resource | `dibs adopt` | Leases a resource you already have, explicitly. Unleased resources are never swept. | The same as its adapter |
| Running shell (opt-in) | `shell`, with `DIBS_SHELL_ADAPTER=1` | Leases a shell PID and its helper processes. Release sends SIGHUP, then SIGTERM and SIGKILL to survivors. | `adopt shell` on Linux or macOS |
| Agent terminal (opt-in) | `tmux`, `herdr`, `rex`, or `treehouse-subshell`, with `terminal_adapters` in user config or `DIBS_TERMINAL_ADAPTERS` | Leases a pane, block, or subshell to its task owner; sweep never closes it while its owner is alive. | The corresponding terminal tool |
| Pooled git worktree (opt-in) | `treehouse-worktree`, with `DIBS_TREEHOUSE_WORKTREE=1` | Leases one slot of a treehouse pool. Release refuses to return a slot while its work has not landed. | treehouse 3.1.5 or newer, git 2.31 or newer |
| Local job | `dibs job` | Admits the job within this machine's CPU and memory budget, minus owner room. | Nothing |
| Job on an SSH machine | `dibs job` with a configured machine | Runs the job on a configured SSH machine under that machine's budget. | SSH access to the machine |

Details for each resource are in [docs/guides/resources.md](docs/guides/resources.md)
and [docs/guides/jobs.md](docs/guides/jobs.md).

## Lease agent terminals

Turn on only the terminal adapters you use with `terminal_adapters` in your user
`config.json`, or set `DIBS_TERMINAL_ADAPTERS` to a comma-separated list. Right
after opening a pane, block, or subshell, lease it to the opening task; keep the
returned lease ID and release it when closing the terminal:

```sh
dibs orphans --type tmux                          # unleased panes, with an adopt template each
dibs adopt <type> <id> --owner KIND:ID --ttl D
dibs release <lease-id> --confirm
```

Add `--server` when needed (rex accepts only a local server socket, because block
PIDs from another host are never signalled here) and optionally `--key` for an
idempotent retry. Dibs
does not hook terminal creation: skipped adopts leave terminals unleased, and
`dibs orphans --type <type>` lists one kind's unleased terminals for explicit
adoption without touching Docker. Plain `dibs orphans` lists every enabled kind;
when Docker is unreachable it reports `docker` under `unavailable` instead of
failing. Preview cleanup with `dibs sweep`; `dibs sweep --confirm` acts only on
eligible leased terminals.

AXI and JSON return the lease as `lease.id` and terminal identity as
`lease.terminal` (`server`, `pid`, `start`; treehouse subshells also include
`children[]`, each with `pid` and `start`). Same-owner adoption retries return
the existing lease; use `dibs extend` to renew it.

## Group a unit of work (opt-in)

With `"scopes": true` in your user `config.json`, or `DIBS_SCOPES=1`, a scope groups the leases of one task, session, or job and owns them. Pass your own id for the work as `--key`, so a restart gets the same scope back:

```sh
dibs scope start --key task-7 --owner KIND:ID
dibs create --adapter kind --owner scope:<id> --ttl 2h
dibs scope show <id>
dibs scope end <id> --confirm
```

A lease owned by `scope:<id>` lives while the scope's owner is alive and the scope is unexpired, or, for an owner-bound scope, past its TTL. See [Scopes](docs/leases.md#scopes) and [lease policies](docs/guides/owners.md#lease-policies).

## Reclaim when the owner exits (opt-in)

Set `"lease_watcher": true` in your user `config.json`, or `DIBS_LEASE_WATCHER=1`.
Then `create` and `adopt` start a small watcher for each lease owned by a
`pid:` or `session:` process. It blocks on that process's exit, runs the
ordinary confirmed sweep for that one lease, and exits; it also exits once the
lease is released. Policy holds still apply, so an `action: report` policy only
reports. Expiry never wakes it, so keep `dibs sweep --confirm` on a host timer
as the backstop. Details: [docs/leases.md](docs/leases.md#lease-watcher).

## Hand a lease to a new owner (opt-in)

With `"handoff": true` in your user `config.json`, or `DIBS_HANDOFF=1`, a lease
can pass to a new owner, such as a worktree passing from its task scope to its
pull request. The new owner must be alive. The resource and expiry stay; with
lease policies on, the lease takes its new owner's row, and with them off, its
recorded policy stays. A lease handed out of a scope is no longer released by
`dibs scope end`:

```sh
dibs handoff <id> --owner 'pr:acme/app#42'            # preview
dibs handoff <id> --owner 'pr:acme/app#42' --confirm  # retries report already_done
```

`dibs explain <id>` shows the last handoff. See
[Handoff](docs/leases.md#handoff).

## Safety

- dibs acts only on resources that a lease covers. Unleased resources are never
  swept, and adoption is an explicit `dibs adopt`.
- Reclaim needs proof: a dead owner or an expired lease. Unknown owner liveness
  alone never reclaims, and a host acts only on its own leases.
- A lease is forgotten only after its resource is destroyed and verified gone,
  except the documented [process-group exception](docs/adr/0002-reclaim-with-proof.md#existing-process-exception).
  Cleanup intent is saved before the resource is created.

dibs is a cooperation protocol, not a security boundary against programs running
as the same Unix user. The full rules are in [docs/guides/state-and-safety.md](docs/guides/state-and-safety.md).

## Documentation

User guides are in [docs/guides](docs/guides/README.md): install, resources, owners,
jobs, output and agents, the command reference, and safety. Contributors start at
[docs/README.md](docs/README.md).

## License

Licensed under [Apache-2.0](LICENSE). See [NOTICE](NOTICE).
