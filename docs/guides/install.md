# Install

Pick one. Each installs the same `dibs` program.

- **Go** (Go 1.26 or later): `go install github.com/Dematom1/dibs@latest`.
  The module path is case-sensitive; `github.com/dematom1/dibs` fails.
- **Nix**: `nix run github:Dematom1/dibs -- --version` runs it, and the flake
  builds `dibs` for `x86_64-linux`, `aarch64-linux`, and `aarch64-darwin`.
- **Release binary**: Linux and macOS archives, amd64 and arm64, on the
  [GitHub Releases](https://github.com/Dematom1/dibs/releases) page. Check each
  archive against `checksums.txt` before you run it. Release macOS binaries are
  not Developer ID signed, so Gatekeeper may quarantine them. After the checksum
  passes, `xattr -d com.apple.quarantine /path/to/dibs` clears the flag on that
  binary. Do not disable Gatekeeper globally.

`dibs --version` prints the version without the leading `v`, for example `0.2.4`.
Builds from a checkout report a commit-based version instead. From a checkout,
`just build` writes `bin/dibs`, and `just install` installs it to Go's binary
directory.

The quickstart needs no Docker. kind, k3d, and Compose leases need their own
tool (`kind`, `k3d`, or `docker compose`) on `PATH`, plus a running Docker engine.

## Upgrade from berth

Dibs is the new name for berth. Update install commands, sweep timers, scripts,
and remote binary paths to `dibs`. The Nix package and release archives
provide a deprecated `berth` command through the 0.3 releases; both it and the
`BERTH_*` aliases are removed in 0.4.0. `berth` is the same program as `dibs`,
with identical stdout and exit codes, plus one stderr deprecation line naming
`dibs`. Existing berth-named leases remain reclaimable. See the [rename release
notes](../releases/rename-to-dibs.md) for compatibility details and
repository-rename ordering.

