#!/usr/bin/env bash
# Upgrade loom to the latest release (or LOOM_VERSION) by re-running the
# installer. Honors the same LOOM_PREFIX / LOOM_BINARIES overrides.
#
#   curl -fsSL https://raw.githubusercontent.com/bgrewell/loom/main/scripts/upgrade.sh | bash
set -euo pipefail

REPO="bgrewell/loom"
INSTALL_URL="https://raw.githubusercontent.com/$REPO/main/scripts/install.sh"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

if command -v loom >/dev/null 2>&1; then
	info "current: $(loom --version 2>/dev/null | head -1)"
else
	info "loom not currently installed; installing fresh"
fi

# Re-run the installer for the target version (default: latest). install.sh is
# idempotent and overwrites the existing binaries in place.
info "upgrading to ${LOOM_VERSION:-latest}…"
curl -fsSL "$INSTALL_URL" | LOOM_VERSION="${LOOM_VERSION:-latest}" bash
