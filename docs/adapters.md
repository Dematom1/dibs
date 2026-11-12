# Adapters, adoption, and orphans

An adapter creates and destroys the resource behind a lease. dibs ships five built-in adapters, `none`, `kind`, `k3d`, `compose`, and the opt-in `treehouse-worktree`, each registered explicitly in `internal/cli/adapters.go`. The job reservation uses a fifth, internal `process` path that is not in the registry. This page describes the adapter contract, each built-in adapter, how dibs pins the Docker endpoint for cleanup, and how existing resources enter dibs through `adopt` and are listed through `orphans`.

Source: `internal/adapter/adapter.go` (contract and registry), `internal/adapter/command.go` (external commands), `internal/adapter/docker.go` and `internal/adapter/inventory.go` (Docker context and discovery), `internal/adapter/{none,kind,k3d,compose,treehouseworktree}/` (built-ins), `internal/lease/inventory.go` (`Adopt`, `Orphans`), and `internal/cli/inventory.go` (the commands). Tests: `internal/adapter/adapter_test.go`, `internal/adapter/compose/inventory_test.go`, `internal/adapter/snapshot_test.go`, `internal/lease/inventory_test.go`, `internal/cli/inventory_test.go`, and `internal/cli/integration_test.go` (build tag `integration`, run with `just integration`). Decisions: [ADR 0002](adr/0002-reclaim-with-proof.md), [ADR 0005](adr/0005-adapter-and-owner-extension-contracts.md).

## Contract

`internal/adapter/adapter.go` defines the interface that every built-in implements:

```go
type Adapter interface {
	Create(context.Context, Target) (ResourceRef, error)
	Destroy(context.Context, Target) error
}
```

A `Target` carries the `ResourceRef` captured at prepare time, the lease `Metadata` (lease ID, machine, owner, expiry, and labels), and `DiscardUnlanded`, which only an explicit `release --discard-unlanded` sets. `Create` returns the identity to record; it differs from the prepared one only when creation learns something, such as an allocator token. The lease manager records it, never the adapter. A `Definition` adds what the core needs around the adapter: `Name`, `CreateAllowed`, `RequiresFile`, `OptIn` (the setting that enables an optional built-in that is off), `VerifiesAdoption` (its `Prepare` verifies an existing resource without Docker discovery), `CleanupOutsideLock` (its `Destroy` runs without the state lock), and the functions `Prepare`, `Validate`, `Env`, and optionally `Discover`. `NewRegistry` panics on an incomplete or duplicate definition, so a broken adapter fails at startup, not on first use.

The callers in `internal/lease/manager.go` and `internal/lease/lease.go` enforce the contract:

- `Prepare` runs before the lease is saved, and captures the Docker context and endpoint and the private files. See [Docker endpoint pinning](#docker-context-pinning).
- `Create` runs only after the lease is saved. A failed `Create` keeps the lease. A changed identity from `Create` must pass `Validate` and is saved before the result is reported.
- `Destroy` tolerates a resource that is partly created or already gone.
- `Destroy` returns nil only after it has verified that the resource is gone (verify-gone). `kind`, `k3d`, and `compose` delete the resource and then list what remains. `none` has nothing to verify.
- A `Destroy` error keeps the lease, and a later `release` or `sweep` retries it.

`releaseLease` refuses a lease whose adapter is not in the registry and keeps the lease. An older binary therefore never drops a record it cannot clean up.

`adapter.go` notes that the verify-gone check currently lives inside each built-in `Destroy`. A separate `VerifyGone` contract belongs to the planned extension work. See [Planned](#planned).

## Running external tools

`RunCommand` in `internal/adapter/command.go` runs every external tool call for `kind`, `k3d`, and `compose`:

- Each call has a 5-minute timeout.
- Stdin is empty.
- Stderr is captured. On failure, dibs prints it to stderr and returns a generic error that tells the user to check the tools and retry `dibs release` or `dibs sweep --confirm`.

Tests inject a fake `CommandRunner` through each adapter's `New(run)` to check the exact argv and environment. Test: `TestAdapterCommandsAndSurvivors`.

## Docker context pinning

`DockerContext` in `internal/adapter/docker.go` runs before the lease is saved, for every Docker-backed adapter:

1. It refuses to run when `DOCKER_HOST` is set. Users select a named Docker context instead.
2. It records the current context from `docker context show`. An empty context is an error.

The `Prepare` function of each Docker-backed adapter then:

1. Creates `<state>/resources/<id>/` with mode `0700`.
2. Sets the resource name to `dibs-<lease id>`, or `dibs-<first 26 ID characters>` for k3d, which limits cluster names to 32 characters.
3. For k3d only, records the context's endpoint from `docker context inspect` as `docker_host`. kind and Compose record the context name and never an endpoint.
4. For Compose, writes the resolved config snapshot described under [Compose](#compose).

Later commands use the pinned context name, not the caller's current environment. kind and Compose resolve the endpoint from that named context on every call, so if the context is later pointed at another daemon, cleanup targets that daemon. k3d is the only adapter that pins the endpoint itself:

- `DockerEnv` (kind, Compose, and discovery) sets `DOCKER_HOST=` (empty), `DOCKER_CONTEXT=<recorded context name>`, `KIND_EXPERIMENTAL_PROVIDER=docker`, and `COMPOSE_INTERACTIVE_NO_CLI=1`.
- `k3dEnv` sets `DOCKER_CONTEXT=` (empty), `DOCKER_HOST=<pinned endpoint>`, `KIND_EXPERIMENTAL_PROVIDER=docker`, and `KUBECONFIG=<private file>`.

## Built-in adapters

### none

`none.New` does nothing on `Create` and `Destroy`. The lease is metadata only, and dibs never touches an external resource for it. It is the default for `dibs create`.

### kind

- `Create`: `kind create cluster --name <name> --kubeconfig <private file> --wait 120s`.
- `Destroy`: `kind delete cluster`, then `kind get clusters`. If the name is still listed, `Destroy` returns an error.
- The kubeconfig is private to the lease, so dibs never edits the caller's kubeconfig or current context. `dibs env <id>` prints the `KUBECONFIG` export.

Test: `TestIntegrationKind`.

### k3d

- `Create`: `k3d cluster create <name>` with `KUBECONFIG` set to the private file.
- `Destroy`: `k3d cluster delete <name>`, then `k3d cluster list -o json`. If the name is still listed, or the list cannot be parsed, `Destroy` returns an error.

Tests: `TestK3dKeepsCallerKubeconfig`, `TestIntegrationK3d`.

### Compose

`prepare` in `internal/adapter/compose/compose.go` resolves the user's Compose file once with `docker compose config --format json` and rewrites it:

- The config must define at least one service.
- A service with a fixed `container_name` is refused, unless the lease is adopted.
- A volume or network with a fixed `name` other than `<project>_<key>` is refused, unless it is marked `external`.
- Every non-external service, volume, and network gets the labels `io.dibs.lease`, `io.dibs.owner`, `io.dibs.expires`, and `io.dibs.machine`.
- The result is written atomically to `<state>/resources/<id>/compose.json`.

Cleanup reads only this snapshot. It does not need the original file, environment, or worktree. The `io.dibs.expires` label records the expiry at creation. After `extend`, the lease record is authoritative.

- `Create`: `docker --context <ctx> compose -p <name> -f compose.json up -d`.
- `Destroy`: `compose down --volumes --remove-orphans`. Then `docker ps -aq`, `docker volume ls -q`, and `docker network ls -q`, each filtered by `label=com.docker.compose.project=<name>`. Any output is an error.

`down --volumes` deletes data in managed volumes. External volumes and networks are outside cleanup.

Tests: `TestComposeSnapshotAndUnsafeNames`, `TestIntegrationCompose`.

### process

The `process` adapter backs job reservations. It is not in the registry. `processResource` in `internal/lease/job_process.go` handles it inside `releaseLease`. `Create` does nothing. `Destroy` terminates the job's process group after it verifies the leader's identity. If the leader is already gone and reaped, `Destroy` succeeds without signaling, and descendants may survive. [ADR 0002](adr/0002-reclaim-with-proof.md#existing-process-exception) records this exception. See [Jobs](jobs.md#process-group-cleanup).

### treehouse-worktree

`treehouseworktree.New` leases one slot of a treehouse worktree pool through treehouse's own durable lease token. It is registered on every run so recorded leases stay readable and releasable, but `CreateAllowed` and `VerifiesAdoption` are set only when `treehouse_worktree_adapter` or `DIBS_TREEHOUSE_WORKTREE` turns it on. Every call goes through `RunCommandIn` with the pinned main checkout as its working directory, the 5-minute timeout, and an environment that clears `TREEHOUSE_ROOT` and `TREEHOUSE_LEASE_HOLDER`. A pinned pool root is passed as `--root`.

- `Prepare` resolves `--repo` to the main checkout with `git rev-parse --path-format=absolute --git-common-dir`, pins `TREEHOUSE_ROOT` when it is set, records `--landed-exclude-remote` names, and sets the holder to `dibs:<lease id>`. It writes no snapshot file.
- `Create`: `treehouse get --lease --json --lease-holder dibs:<id>`. It returns the slot path and `lease_id`, plus the slot name from `treehouse status --json` when that call succeeds. An unreadable allocation keeps the pending lease, which `Destroy` finds by its holder.
- `Destroy` lists `treehouse status --json` and finds the claim by token, or by holder while no token is recorded. With no claim left, it succeeds only when the pinned slot is listed without the claim or no longer exists; an unlisted slot that still exists is an error. Before those checks, it refuses with an `UnlandedError` if `treehouse status --json` reports processes in the slot. Treehouse detects processes by cwd inside the slot; processes whose cwd is elsewhere are outside this proof. With no detected processes, it refuses unlanded work using `git status --porcelain --untracked-files=all` and `git log --format=%H HEAD --not [--exclude=<remote>/*]... --remotes`. Immediately before return it rechecks the same claim, process list, and `git rev-parse HEAD`; any change retains the lease. `--discard-unlanded` skips these refusals. Then it runs `treehouse return <path> --if-lease-id <token>` (with `--if-lease-holder dibs:<id>` for slots it allocated, and `--force` only for `DiscardUnlanded`) and lists again to verify that the claim is gone. Treehouse's exit 3 (it kept a dirty slot) keeps the lease.
- Adoption: `Prepare` with `Existing.Adopted` requires the slot at that path to be `leased` under exactly that token, and refuses a path, token, or pending holder that another lease in `PrepareRequest.Leased` already owns. Adopted slots record no holder, so their return is conditional on the token only.
- `CleanupOutsideLock` is set, because a return waits out treehouse's process grace period and resets the tree. See [Leases](leases.md#release).

Tests: `internal/adapter/treehouseworktree/treehouseworktree_test.go`, `internal/cli/treehouse_test.go`, and `TestIntegrationTreehouseWorktree` (real treehouse in a disposable repository and pool; it fails when treehouse is missing).

## Adoption

`dibs adopt <kind|k3d|compose> <name> --owner KIND:ID --ttl D` takes an existing resource under a lease. `dibs adopt treehouse-worktree <path> --lease-id <token>` needs no Docker context; for it, step 3 and 4 below are replaced by the adapter's own verification against a read-only view of the same adapter's leases (see [treehouse-worktree](#treehouse-worktree)). `Manager.Adopt` in `internal/lease/inventory.go` does the following under the state lock:

1. The owner must be alive, and the TTL must be positive.
2. The adapter must support discovery, the name must be valid, and `--file` must be present exactly when the adapter requires it (Compose).
3. No existing lease may already own the resource. `resourceLeased` compares adapter and name, and resolves other Docker contexts to rule out a shared endpoint.
4. The resource must exist. Discovery finds it by its project or cluster label.
5. The adapter's `Prepare` captures its cleanup scope with `Adopted` set. kind and k3d write their kubeconfig from `kind get kubeconfig` or `k3d kubeconfig get`. Compose verifies adoption.
6. The lease gets `io.dibs.*` labels, and the state is saved at version 2.

Adoption does not recreate the resource and does not change its name. The adopted lease is then an ordinary lease: `dibs release <id> --confirm` runs the adapter's `Destroy` and deletes the resource. Adoption therefore carries the same destructive cleanup as a created lease.

For Compose, `verifyComposeAdoption` in `internal/adapter/compose/inventory.go` refuses an existing non-external volume or network that is not labeled with the project. Mark shared resources `external` before adoption. Tests: `TestAdoptRefusalsAndDurability`, `TestComposeAdoptionRequiresProjectLabels`, `TestIntegrationAdoptAndOrphans`.

## Orphans

`dibs orphans [--context NAME]` lists resources that no lease owns. `Manager.Orphans` runs discovery for every adapter that can create, then drops every resource that `resourceLeased` matches.

- Discovery (`DiscoverDocker`) finds containers (`-a`), and for Compose also volumes and networks, by their project or cluster label. It groups them by name and reports labels, creation evidence, and the Compose working directory with its status.
- The lease check reads a snapshot of `state.json` with `readInventoryStore`. That read takes no lock, writes nothing, and does not migrate state. Its answer is point-in-time evidence.
- Each row includes an `adopt` command template. The template uses placeholders for the owner and TTL, so the operator makes both choices.

Unleased does not mean unused. Age never authorizes cleanup. Test: `TestReadOnlyInventoryAndStateMigration`, and `TestInventoryOutputModes` for the output.

## Docker endpoint for legacy and adopted leases

`validate` in each Docker-backed adapter accepts the legacy `berth-<id>` name for existing leases, so leases created before the rename still list and reclaim. For k3d, the legacy name is `berth-` plus the first 26 characters. Adopted leases accept any valid resource name and require a captured `docker_host`. Test: `TestLegacyAndAdoptedSnapshotsThroughStateAlias`. Compose cleanup uses the recorded project, not label discovery. Test: `TestLegacyComposeLabelsCleanedByRecordedProject`.

## Invariants

1. **A record is removed only after verify-gone.** `Destroy` returns nil only after it lists the remaining resources and finds none. A new adapter must do the same. The only exception is the `process` adapter's reaped-leader case in ADR 0002, and a change to it needs its own decision.
2. **`Destroy` tolerates absence.** An already-absent or partly created resource is not a reason to keep failing, so retries converge. Test: `TestSweepPreviewRetryAndIdempotence`.
3. **Cleanup never depends on the caller's environment.** Adapters use the context name (and, for k3d, the endpoint) and private files recorded in the lease, never the current `DOCKER_HOST`, `DOCKER_CONTEXT`, `KUBECONFIG`, or the original Compose file. The recorded context name is not a pinned endpoint: changing what that context points to changes what cleanup targets. Test: `TestK3dKeepsCallerKubeconfig`.
4. **dibs touches only resources it named.** Names derive from the lease ID, or come from an adoption the operator made explicitly. Compose rejects fixed names that cleanup could share with another project. dibs never sweeps unleased resources. Test: `TestComposeSnapshotAndUnsafeNames`.
5. **Every external call is bounded.** `RunCommand` applies a 5-minute timeout.
6. **No secrets in arguments.** Resolved Compose values go to the private `compose.json` file, not to argv.
7. **Each adapter's file stays in its state directory.** `Validate` checks the file with `SnapshotMatches`, so cleanup cannot point at a file outside `<state>/resources/<id>/`.

## Planned

[ADR 0005](adr/0005-adapter-and-owner-extension-contracts.md) plans these changes. None exists in the code today.

- A versioned Adapter contract with `create`, an idempotent `destroy`, `exists` (verify-gone), and `describe`. Today, verify-gone lives inside each `Destroy`.
- A conformance kit, `dibs adapter test <name>`, that checks idempotent destroy, tolerance of already-absent resources, truthful verify-gone, honored timeouts, and applied labels. The command does not exist.
- An `exec` adapter defined in a local file, with create, destroy, and exists commands and a timeout.
- Out-of-process `dibs-adapter-<name>` plugins on `PATH` that speak a JSON protocol over stdin and stdout. Plugins never touch dibs state.
- Optional path or checksum pinning for plugins, and a community catalog.
- Label application is Compose-specific today. The planned contract applies required labels to every managed resource.
- Governance: a built-in adapter must be widely used and testable in CI. Everything else ships as `exec` or a plugin.
