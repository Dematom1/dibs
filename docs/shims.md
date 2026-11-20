# Create shims

`dibs setup shims` routes an agent task's resource creates through `dibs create`, so a task cannot start a Compose project or a cluster that no lease covers. It writes thin PATH shims for `docker` (including `docker compose`), `kind`, `k3d`, `orb`, and `orbctl`. The shims do nothing for a human: without `DIBS_TASK` they exec the real tool unchanged. This page describes the shim script, how a call is classified, and the rules that keep the shims safe.

Source: `internal/cli/shims.go` (`runSetupShims`, `shimScript`, `runShim`, `classifyShim`). Tests: `internal/cli/shims_test.go` and `TestIntegrationShimCompose` in `internal/cli/integration_test.go`.

## Opt-in

The feature is off until `"shims": true` is in the user `config.json` or `DIBS_SHIMS=1` is set. `DIBS_SHIMS` overrides the config either way (`shimsEnabled`). With the opt-in off, `dibs setup shims` exits 2 and writes nothing, and an installed shim passes every call through, even with `DIBS_TASK` set. The `setup` help and the command list mention shims only while the opt-in is on, so the default help is unchanged.

Installing the shims never turns interception on. dibs never edits `PATH`. The installer, for example a dotfiles home configuration, puts the shim directory first on `PATH` for agent sessions.

## Setup

`dibs setup shims [--dir D]` writes one script per tool into `D`. The default is `$XDG_DATA_HOME/dibs/shims`, or `~/.local/share/dibs/shims`. Each script has the marker line `# dibs-shim: managed by dibs setup shims` and mode `0700`.

`runSetupShims` checks every target before writing. If a file exists without the marker, it exits 1 and changes nothing. Writes use `atomicfile.Write`. A repeated run reports `unchanged (no-op)`. Test: `TestSetupShims`.

## The shim script

The script is POSIX `sh`. It has the absolute path of the shim (`self`) built in.

1. It walks `PATH`. It finds the last entry whose `$tool` is the same file as `self` (`test -ef`), then picks the first executable `$tool` after it. If `self` is not on `PATH`, it picks the first executable `$tool` that is not `self`. If nothing qualifies, it exits 127 with `dibs shim: no <tool> on PATH after <self>`.
2. Without `DIBS_TASK`, it runs `exec "$real" "$@"`. Argv, stdin, stdout, stderr, and the exit status are the real tool's own.
3. With `DIBS_TASK` set, it runs `exec dibs __shim <tool> <real> "$@"`, using the `dibs` on `PATH`. If `dibs` is missing, it exits 127.

Every hop moves to a strictly later `PATH` entry. So a shim never execs itself, and stacked or repeated shim directories still reach the real tool. Tests: `TestShimPassesThroughWithoutTask` and `TestShimHandsTaskCallsToDibs`.

## Classification

`__shim` is a hidden command and not part of the CLI contract. If `DIBS_TASK` is empty or the opt-in is off, it execs the real tool. Otherwise `classifyShim` sorts the call into one of three results:

| Call | Result |
| --- | --- |
| `docker compose [-f FILE] up [-d \| --detach]` | `dibs create --adapter compose --file FILE` |
| `kind create cluster` | `dibs create --adapter kind` |
| `k3d cluster create` | `dibs create --adapter k3d` |
| `docker compose ... up` on a project named `dibs-<id>` that is a compose lease in this ledger | Passes through. `isComposeLease` checks this. |
| Any other `docker compose up`, `kind create cluster`, or `k3d cluster create` | Refused with exit 2 |
| `docker run -d`, `docker container run -d` | Refused with exit 2. dibs has no single-container adapter, so the hint names `dibs create --adapter compose --file <file>`. |
| `orb create`, `orbctl create` | Refused with exit 2. dibs has no OrbStack adapter. |
| Anything else, including `--help` | Passes through |

A create is leased only when dibs can honor it exactly. dibs names the project or cluster `dibs-<id>`. It runs Compose detached from one resolved file and creates clusters with their own private kubeconfig. So these creates are refused, and the hint names the `dibs create` command to run instead:

- A custom project or cluster name, including `-p` or `COMPOSE_PROJECT_NAME`.
- Service names.
- Any `docker` global option, Compose option, or `up` flag other than `-d`.
- Several files, or `-f -`.
- A default file that Compose would merge with an override file.

Compose file lookup follows `docker compose`. It uses `-f`, then `COMPOSE_FILE` (one path only), then the first default name (`compose.yaml`, `compose.yml`, `docker-compose.yml`, `docker-compose.yaml`) in the working directory or its nearest parent. Relative paths resolve against the working directory.

For `docker run`, `classifyRun` reads options up to the image. A short group such as `-dit` counts `d`. A letter that takes a value, such as `-e` or `-v`, ends the group. Unknown long flags without `=` take a value, so arguments after the image never count.

Test: `TestClassifyShim`.

## The leased create

`runShim` builds `dibs create --adapter <a> [--file F] --resources "<original command>" --label task=$DIBS_TASK`. It prints that command on stderr and runs it in the same process. The output and exit code are `dibs create`'s own.

The owner is:

1. `DIBS_OWNER`, if set. `dibs create` reads it itself.
2. Otherwise `scope:$DIBS_SCOPE`.
3. Otherwise `scope:<id>` of the scope whose key is `DIBS_TASK`, when scopes are on.

With no owner, the create is refused with exit 2, and the hint is `dibs scope start --key <task> --owner KIND:ID`. A shim never falls back to `pid:<parent>`, because an agent's short-lived tool shell would leave the lease with a dead owner.

Before the create, `runShim` unsets `DIBS_TASK`. The adapter's own `docker`, `kind`, and `k3d` calls, and `kind`'s own `docker` calls, then pass through the shims. Test: `TestIntegrationShimCompose` (needs Docker).

## Invariants

1. **Humans are unaffected.** Without `DIBS_TASK`, a shim is a plain `exec` that never starts dibs. Test: `TestShimPassesThroughWithoutTask`.
2. **Opt-in.** With `DIBS_SHIMS` or `shims` off, every call passes through. Test: `TestRunShim`.
3. **No recursion.** Each hop moves strictly later on `PATH`, and dibs's own tool calls run without `DIBS_TASK`.
4. **Argv and exit codes are exact** on passthrough. Test: `TestShimPassesThroughWithoutTask`.
5. **A create is never silently changed.** A create runs as a lease or is refused with the `dibs` command to run. It never passes through unleased. The only exception is a project that is already a dibs compose lease.
6. **Unmanaged files are never replaced.** Test: `TestSetupShims`.

Absolute-path calls such as `/usr/local/bin/docker` bypass `PATH` and the shims. A harness hook that rejects those calls belongs to the agent configuration, not to dibs.
