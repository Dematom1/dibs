# Safety

## State and safety

`DIBS_STATE` defaults to `$XDG_STATE_HOME/dibs`, or
`~/.local/state/dibs` when XDG is unset. An explicit `DIBS_STATE` must be an
absolute path; dibs refuses relative paths rather than create state in the
working directory. State files and snapshots are private to
the invoking user. State contains resolved Compose values, which may include
secrets. Keep this directory on a local filesystem, not a network share.

State schema version 2 adds explicitly adopted resource identities and stable
machine identities. Any command that opens version 1 state under the state lock
migrates it to version 2, preserving existing lease records (see below).
Read-only `orphans` inventory reads a snapshot without migrating it. Older
binaries reject version 2 rather than interpreting adopted names as generated
ones. Keep a compatible binary while leases remain. State records the version
of the dibs binary that last saved it as `writer_version`. A binary that
records `writer_version` and finds a state version newer than it supports exits
1, keeps the state unchanged, and names the version found, the recorded writer
version, and the newest version it supports. Upgrade dibs, and run
`which -a dibs berth` to find a stale binary earlier on `PATH`. Binaries built
before `writer_version` existed report `unsupported or incomplete state format`
for the same state and do not name the versions.

State schema version 3 is version 2 plus at least one lease with live-owner
protection. Older binaries reject it rather than ignoring the protection and
reclaiming the lease at expiry. The state returns to version 2 once no
protected lease remains, unless a keyed or pending lease keeps it at version 4.

State schema version 4 adds the consumer contract's idempotency keys and pending
leases. Dibs writes version 4 while any lease has a key or is pending, version 3
while only protected leases remain, and version 2 otherwise. A binary that reads
up to version 3 would drop keys and pending records when it saved, so it
refuses version 4 instead.

State schema version 5 adds recorded lease policies, owner-death stamps for grace
periods, and shell leases. Dibs writes version 5 while any lease has one of
them, and the lower versions above otherwise. A binary that reads up to version
4 would reclaim an owner-bound lease at expiry, so it refuses version 5 instead.

A stable `lock` file serializes ordinary readers and writers through kernel `flock`.
`orphans` reads an atomic state snapshot without locks or writes. Its answer is
point-in-time evidence, not a reservation. `adopt` rechecks ownership under the
state lock.
Each state change writes and syncs a temporary file, renames it over `state.json`,
and syncs the directory. Dibs saves cleanup intent **before** starting a
resource. If dibs dies midway through create or delete, the next release or
eligible sweep retries the same unique resource. Kernel locks end on process
death. Corrupt or unsupported state is an error, never an empty lease list.

Each new lease records a stable machine identity: `darwin:<IOPlatformUUID>` on
macOS or `linux:<machine-id>` on Linux, normalized to lowercase hexadecimal
without hyphens. Hostname changes no longer change lease ownership. An unavailable
or invalid OS identity is an error; dibs never falls back to a hostname.
Cloned hosts must have distinct OS machine identities. On minimal Linux hosts
without `/etc/machine-id`, provision a unique persistent ID before running dibs
(for example, `systemd-machine-id-setup` on systemd hosts). Dibs does not generate
or modify OS identities. Keep state on its original host, and do not copy machine
IDs between hosts.

Opening existing state migrates schema version 1 to version 2 atomically under
the state lock, including during previews and listing. Legacy lease and queue
records with the current hostname receive the stable identity. Other legacy
hostnames remain foreign until explicitly identified by you. Fully migrated
version 2 state does not require a hostname lookup. Older binaries that only
support schema version 1 cannot read version 2 state; use a schema-compatible
binary for every invocation. The naming migration does not change the schema
or bypass machine-identity migration.

If this machine was renamed before upgrading, first verify that the old hostname
belongs to this machine and that you are using its original local state directory.
Then authorize that exact legacy hostname through the invocation's environment:

```sh
DIBS_LEGACY_MACHINE=Mac.localdomain dibs sweep  # migrate and preview
dibs sweep --confirm                              # separate cleanup decision
```

`DIBS_LEGACY_MACHINE` is an explicit same-user attestation, not automatic proof
from the old ledger. Never set it from a project file or use it for another host.
The migration persists the stable identity, so later commands need no alias.
The alias affects only hostname records, never another host's stable identity.
Unrecognized legacy names remain unknown and cannot be reclaimed, even if expired.

A host never acts on another machine's leases, even after expiry. Expiry overrides
unknown **owner** liveness only after machine ownership matches. Run cleanup on
the original machine with its original state path, directly or through
`dibs sweep --machine <name> --confirm`. Human, AXI, and JSON output all use the
same machine identity and reclamation decisions.

Adapters pin the Docker context selected at creation. `DOCKER_HOST` overrides
are rejected; select a named context instead. Keep that context's endpoint
unchanged while leases exist. Dibs never sweeps unleased resources. This slice
does not reconstruct leases after state loss; keep the state directory until
all managed resources are released. Small resource snapshots remain after
release for diagnosis and may be removed once their leases are absent.

Dibs is a cooperation protocol, not a security boundary for programs with the
same Unix user. SSH session-as-liveness does not isolate untrusted code.
Worktree transport and Kubernetes-backed machines are deferred. A resident
scheduler is not required for these leases; revisit it only when measured queue
fairness or push-event requirements cannot be met by CLI invocations and host
timers.

