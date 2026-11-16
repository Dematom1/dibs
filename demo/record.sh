#!/usr/bin/env bash
# Renders demo/demo.gif from a released dibs binary, never from the working tree.
# Usage: demo/record.sh [version]   (default: 0.2.4).
# Needs vhs (https://github.com/charmbracelet/vhs) and jq on PATH.
set -euo pipefail

for tool in vhs jq shasum; do
  command -v "$tool" >/dev/null || { echo "missing required tool: $tool" >&2; exit 1; }
done
version="${1:-0.2.4}"
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
archive="dibs_${version}_${os}_${arch}.tar.gz"
base="https://github.com/Dematom1/dibs/releases/download/v${version}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
curl -fsSL -o "$work/$archive" "$base/$archive"
curl -fsSL -o "$work/checksums.txt" "$base/checksums.txt"
(cd "$work" && grep " ${archive}\$" checksums.txt | shasum -a 256 -c -)
mkdir "$work/bin" "$work/state" "$work/home/.config"
tar -xzf "$work/$archive" -C "$work/bin" dibs

# The tape runs in a shell that inherits these, so it sees only this binary, an empty state dir,
# and a temporary home, so no user config (such as lease_policies) can change what is recorded.
cd "$repo_root"
HOME="$work/home" XDG_CONFIG_HOME="$work/home/.config" DIBS_STATE="$work/state" PATH="$work/bin:$PATH" vhs demo/demo.tape
