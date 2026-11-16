# Contributing

## Develop and verify

Install [just](https://just.systems), or run `nix develop` for Go, just, and
GoReleaser. Run `just` to list recipes.

```sh
 just check
 just integration
 just ssh-integration
 just formal
```

`just formal` runs bounded TLA+ models, real-code checks, and mutation checks.
It requires Java 17 or newer, Python 3, and Go. The model checker download is
version- and checksum-pinned. Read the [formal findings](../../verification/findings.md)
first: TTL expiry may reclaim an Alive owner by policy; the model checks that
an unexpired live owner is not reclaimed. Orphan-group cleanup and a deadline-
check race remain counterexamples, not verified guarantees. CI reproduces those
known failures explicitly. `python3 verification/check.py --strict` exits nonzero
for F2 and F3.
The [lease and job safety skill](../../.claude/skills/lease-job-safety/SKILL.md)
records the code mapping, bounds, assumptions, and rerun procedure.

The project-local [verify skill](../../.claude/skills/verify/SKILL.md) drives the demo
through the real CLI, including Compose HTTP, Ready kind/k3d nodes, FIFO jobs,
and SSH session cleanup. To run every flow and retain proof outside the checkout:

```sh
PROOF=$(mktemp -d "${TMPDIR:-/tmp}/dibs-proof.XXXXXX")
.claude/skills/verify/verify.py all --evidence "$PROOF/all"
```

The helper needs Python 3 and the real adapter/SSH tools; missing prerequisites
fail rather than skip. CI runs these workflows and uploads `dibs-verification`
evidence, including failed runs, for seven days.

`just check` runs formatting checks, vet, race-enabled tests, and build. Unit
tests cover owner death, PID reuse, heartbeat staleness, expiry with a live
owner, unknown liveness, machine boundaries, retry, state corruption, locking,
the CLI lifecycle, budget/owner-room math, FIFO/no-backfill queueing, dead
waiters, and real job subprocesses (child exit codes, deadline/class timeout,
wait exhaustion, cancellation, descendant cleanup, and sweep after owner death).
Real adapter tests are isolated behind the `integration` build tag and run with
`just integration`. They create unique kind, k3d, and Compose resources, then
verify cleanup. They also discover unleased fixtures, adopt them, verify sweep
preview leaves them intact, and reclaim them on expiry through confirmed sweep.
With the consumer contract on, they also record pending leases, build each
resource out of band as a crashed caller would, then reconcile or register it,
check that `reclaim` refuses the live owner, and reclaim it after the owner dies.
The shell test starts a real `bash` login shell on a pseudo-terminal with a
background helper, adopts it under an `exec` owner, shows that reclaim refuses
while the owner lives and while a foreground job runs, and shows that sweep
removes the shell and its helper once the owner is gone; it needs `bash`.
Missing adapter tools, Docker, or Compose fail the command rather
than skip; failed cleanup retains the state directory and reports its path.

`just ssh-integration` runs real SSH end-to-end tests against a disposable
Alpine container with OpenSSH and procps. It requires Docker, SSH, ssh-keygen,
and Go. It fails rather than skips when a prerequisite is absent. Tests use
isolated keys and a strict private known-hosts file. They cover quoting, binary
output, remote budget and owner room, child exit codes including 255, deadline
124, wait 75, session disconnect cleanup, remote listing and sweep, version
refusal, atomic upgrades, and transport failure 76.

`just pr-owner-integration` drives the opt-in `pr` owner against the real,
read-only GitHub API with stable public pull requests: open and draft PRs stay
alive, closed and merged PRs are dead and swept, and a mismatched identity is
unknown and kept. It needs `gh` with authentication or `GH_TOKEN`, and fails
rather than skips without them.

CI runs race-enabled tests and builds on GitHub-hosted `ubuntu-24.04` and
`macos-15`. Linux also runs formatting, vet, dependency integrity, vulnerability
checks, all four release-target cross-builds, and real demo and adapter
integrations. It installs pinned, checksum-verified kind and k3d binaries.
Missing adapter prerequisites fail rather than skip.
The shared contribution workflow provides `contribution / PR title`,
`contribution / PR must be raised via no-mistakes`,
`contribution / Release PR identity`, and
`contribution / Generated files must not be hand-edited`. The Go validation
job is named `main-required-ci`. Required-check policy is managed separately.

`just fmt` formats Go files. `just lint` checks formatting and runs vet.
`just release-check` validates GoReleaser configuration; `just release-snapshot`
builds local archives and checksums without publishing. Release-please opens
version PRs and creates draft releases after merge. CI uploads all four Linux
and macOS archives before publishing a release marked Latest ([#29](https://github.com/Dematom1/dibs/issues/29)).
macOS Developer ID signing is disabled until signing credentials and identity
are configured. Prebuilt macOS binaries are not Developer ID signed and may be
quarantined by Gatekeeper. Prefer `go install github.com/Dematom1/dibs@latest`;
for a downloaded binary, verify its trusted release checksum before removing
quarantine from that binary with `xattr -d com.apple.quarantine /path/to/dibs`.
Do not disable Gatekeeper globally.

Build with `nix build`, or use
`nix-build default.nix -A packages.x86_64-linux.default` without flakes.
The flake provides `packages.<system>.dibs` for `x86_64-linux`, `aarch64-linux`,
and `aarch64-darwin` for NixOS or home-manager package lists. Its binary reports
the version in `.release-please-manifest.json`.

