#!/usr/bin/env bash
# Uninstall loom: remove the loom binaries from the usual install locations.
# Leaves any loomd config, environment files, systemd units, and data untouched.
#
#   curl -fsSL https://raw.githubusercontent.com/bgrewell/loom/main/scripts/uninstall.sh | bash
#
# Override the search with LOOM_PREFIX to target a specific dir.
set -euo pipefail

read -r -a BINS <<<"${LOOM_BINARIES:-loom loomd loomctl}"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }

dirs=()
[ -n "${LOOM_PREFIX:-}" ] && dirs+=("$LOOM_PREFIX")
dirs+=(/usr/local/bin "$HOME/.local/bin")
# Also catch a go-install location if used.
[ -n "${GOBIN:-}" ] && dirs+=("$GOBIN")

removed=0
needsudo=0
for dir in "${dirs[@]}"; do
	for b in "${BINS[@]}"; do
		f="$dir/$b"
		[ -f "$f" ] || continue
		if rm -f "$f" 2>/dev/null; then
			info "removed $f"
			removed=1
		else
			warn "cannot remove $f (permission) — re-run with sudo"
			needsudo=1
		fi
	done
done

if [ "$removed" -eq 0 ] && [ "$needsudo" -eq 0 ]; then
	info "no loom binaries found in: ${dirs[*]}"
fi
info "loomd config / systemd units / data (if any) were left untouched."
[ "$needsudo" -eq 0 ] || exit 1
