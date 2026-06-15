#!/usr/bin/env bash
# loom installer.
#
#   curl -fsSL https://raw.githubusercontent.com/bgrewell/loom/main/scripts/install.sh | bash
#
# Installs the loom binaries (loom, loomd, loomctl). It prefers prebuilt release
# binaries; if none are published for your platform it falls back to building
# from source with `go install` (needs Go).
#
# Environment overrides:
#   LOOM_VERSION   version/tag to install (default: latest release, else source)
#   LOOM_PREFIX    install dir (default: /usr/local/bin as root, else ~/.local/bin)
#   LOOM_BINARIES  space-separated subset to install (default: all three)
set -euo pipefail

REPO="bgrewell/loom"
MODULE="github.com/bgrewell/loom"
read -r -a BINS <<<"${LOOM_BINARIES:-loom loomd loomctl}"
VERSION="${LOOM_VERSION:-latest}"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() {
	printf '\033[1;31merror:\033[0m %s\n' "$*" >&2
	exit 1
}

need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }
need curl
need tar

# --- platform ---
[ "$(uname -s)" = "Linux" ] || die "loom currently supports Linux only (got $(uname -s))."
case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "unsupported architecture: $(uname -m)" ;;
esac

# --- install dir ---
choose_bindir() {
	if [ -n "${LOOM_PREFIX:-}" ]; then echo "$LOOM_PREFIX" && return; fi
	if [ "$(id -u)" -eq 0 ]; then echo /usr/local/bin && return; fi
	if [ -w /usr/local/bin ]; then echo /usr/local/bin && return; fi
	echo "$HOME/.local/bin"
}
BINDIR="$(choose_bindir)"
mkdir -p "$BINDIR" || die "cannot create install dir $BINDIR"

# --- resolve version ---
latest_tag() {
	curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null |
		grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[^"]*"([^"]+)".*/\1/'
}
RESOLVED="$VERSION"
[ "$VERSION" = "latest" ] && RESOLVED="$(latest_tag || true)"

# --- install from a published release tarball ---
install_from_release() {
	local ver="$1" url tmp b
	[ -n "$ver" ] || return 1
	url="https://github.com/$REPO/releases/download/$ver/loom_${ver#v}_linux_${ARCH}.tar.gz"
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' RETURN
	info "fetching $url"
	curl -fsSL "$url" -o "$tmp/loom.tgz" 2>/dev/null || return 1
	tar -xzf "$tmp/loom.tgz" -C "$tmp" 2>/dev/null || return 1
	for b in "${BINS[@]}"; do
		[ -f "$tmp/$b" ] || return 1
	done
	for b in "${BINS[@]}"; do
		install -m 0755 "$tmp/$b" "$BINDIR/$b"
	done
}

# --- build from source ---
install_from_source() {
	command -v go >/dev/null 2>&1 ||
		die "no prebuilt release for ${RESOLVED:-latest} and Go is not installed — install Go (https://go.dev/dl) or set LOOM_VERSION to a released tag."
	local ref="${RESOLVED:-}" b
	for try in "$ref" latest main; do
		[ -n "$try" ] || continue
		info "building from source: go install …@$try"
		if for b in "${BINS[@]}"; do GOBIN="$BINDIR" go install "$MODULE/cmd/$b@$try"; done; then
			return 0
		fi
		warn "go install @$try failed; trying next ref"
	done
	die "could not build loom from source"
}

if install_from_release "$RESOLVED"; then
	info "installed loom $RESOLVED (prebuilt)"
else
	warn "no prebuilt release found for ${RESOLVED:-latest}; building from source"
	install_from_source
fi

# --- verify + PATH hint ---
case ":$PATH:" in
*":$BINDIR:"*) ;;
*) warn "$BINDIR is not on your PATH — add it, e.g.: export PATH=\"$BINDIR:\$PATH\"" ;;
esac
info "installed to $BINDIR:"
for b in "${BINS[@]}"; do printf '    %s\n' "$BINDIR/$b"; done
"$BINDIR/loom" --version 2>/dev/null | head -1 || true
info "done. Next: loom run --help   (docs: https://github.com/$REPO/tree/main/docs)"
