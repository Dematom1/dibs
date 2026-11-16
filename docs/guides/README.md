# dibs user guides

These guides cover installing and using dibs day to day. The [project README](../../README.md) has the pitch, the demo, and a one-minute start.

- [Install](install.md): Go, Nix, release archives, and upgrading from berth.
- [Resources](resources.md): leasing a kind or k3d lab, a Compose project, an existing resource, a running shell, or a pooled worktree.
- [Owners](owners.md): owner kinds, renewal, live-owner protection, and lease policies.
- [Jobs](jobs.md) and [jobs over SSH](ssh-jobs.md): machine-budget admission, deadlines, and remote machines.
- [Output and agents](output-and-agents.md): human, AXI, and JSON output, plus agent hooks.
- [Command reference](command-reference.md): every command, its flags, and its exit codes.
- [Sweep from a host timer](sweep-timer.md): scheduled cleanup on the machine that holds the leases.
- [Safety](state-and-safety.md): state files, machine identity, and the limits of the cooperation model.
- [Contributing](develop-and-verify.md): building, testing, and the verification suites.

Contributors who change behavior also read the [component docs](../README.md), which record the invariants each part must keep.
