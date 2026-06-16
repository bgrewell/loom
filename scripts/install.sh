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
#   LOOM_VERSION        version/tag to install (default: latest release, else source)
#   LOOM_PREFIX         install dir (default: /usr/local/bin as root, else ~/.local/bin)
#   LOOM_BINARIES       space-separated subset to install (default: all three)
#   LOOM_SERVICE        install loomd as a systemd service: 1/yes to install,
#                       0/no to skip. Unset = prompt on a terminal, skip when piped.
#   LOOM_SERVICE_ADDR   LOOMD_ADDR for the service (default ":9551")
#   LOOM_SERVICE_TOKEN  LOOMD_TOKEN for the service (default: none)
#   LOOM_EXAMPLES       install example scenarios: 1/yes (default) or 0/no
#   LOOM_EXAMPLES_DIR   where to install them (default: /usr/share/loom/examples
#                       as root, else $XDG_DATA_HOME/loom/examples)
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

# --- example scenarios ---
choose_exampledir() {
	if [ -n "${LOOM_EXAMPLES_DIR:-}" ]; then echo "$LOOM_EXAMPLES_DIR" && return; fi
	if [ "$(id -u)" -eq 0 ]; then echo /usr/share/loom/examples && return; fi
	echo "${XDG_DATA_HOME:-$HOME/.local/share}/loom/examples"
}

# install_examples fetches the docs/examples scenarios for the installed ref and
# drops them in the examples dir. Best-effort: a failure warns but never aborts
# the install (the binaries are what matter).
install_examples() {
	local dir ref url tmp src
	dir="$(choose_exampledir)"
	ref="${RESOLVED:-main}" # match the installed version; fall back to main for source/latest
	url="https://github.com/$REPO/archive/$ref.tar.gz"
	tmp="$(mktemp -d)" || return 1
	trap 'rm -rf "$tmp"' RETURN
	curl -fsSL "$url" -o "$tmp/src.tgz" 2>/dev/null || {
		warn "could not fetch example scenarios ($ref)"
		return 1
	}
	tar -xzf "$tmp/src.tgz" -C "$tmp" 2>/dev/null || return 1
	# The codeload archive extracts to loom-<ref>/docs/examples.
	src="$(find "$tmp" -type d -path '*/docs/examples' 2>/dev/null | head -1)"
	[ -n "$src" ] || {
		warn "example scenarios not found in $ref"
		return 1
	}
	mkdir -p "$dir" 2>/dev/null || {
		warn "cannot create $dir (try sudo, or set LOOM_EXAMPLES_DIR) — skipping examples"
		return 1
	}
	install -m 0644 "$src"/*.scenario.yaml "$dir"/ 2>/dev/null || return 1
	[ -f "$src/README.md" ] && install -m 0644 "$src/README.md" "$dir"/ 2>/dev/null
	info "installed example scenarios → $dir"
}

case "${LOOM_EXAMPLES:-1}" in
0 | no | NO | false | FALSE) ;;
*) install_examples || true ;;
esac

# --- optional: install loomd as a systemd service (skip the `loomd &` dance) ---
UNIT_PATH=/etc/systemd/system/loomd.service

want_service() {
	# Only relevant when loomd was installed, systemd is present, and we're root.
	case " ${BINS[*]} " in *" loomd "*) ;; *) return 1 ;; esac
	command -v systemctl >/dev/null 2>&1 || return 1
	[ "$(id -u)" -eq 0 ] || return 1

	local want="${LOOM_SERVICE:-}" ans
	if [ -z "$want" ]; then
		# Prompt only when a real terminal is attached; `curl | bash` has the script
		# on stdin, so read from the controlling tty instead.
		if [ -r /dev/tty ]; then
			printf 'Install and start loomd as a systemd service? [y/N] ' >/dev/tty
			read -r ans </dev/tty || ans=""
			case "$ans" in y | Y | yes | YES) want=1 ;; *) want=0 ;; esac
		else
			want=0
		fi
	fi
	case "$want" in 1 | yes | YES | true | TRUE) return 0 ;; *) return 1 ;; esac
}

install_service() {
	local addr="${LOOM_SERVICE_ADDR:-:9551}" token="${LOOM_SERVICE_TOKEN:-}"
	info "installing systemd service → $UNIT_PATH"
	{
		printf '[Unit]\n'
		printf 'Description=loom agent (loomd)\n'
		printf 'Documentation=https://github.com/%s\n' "$REPO"
		printf 'After=network-online.target\n'
		printf 'Wants=network-online.target\n\n'
		printf '[Service]\n'
		printf 'Type=simple\n'
		printf 'Environment=LOOMD_ADDR=%s\n' "$addr"
		[ -n "$token" ] && printf 'Environment=LOOMD_TOKEN=%s\n' "$token"
		printf 'ExecStart=%s/loomd\n' "$BINDIR"
		printf 'Restart=on-failure\n'
		printf 'RestartSec=2\n\n'
		printf '[Install]\n'
		printf 'WantedBy=multi-user.target\n'
	} >"$UNIT_PATH"

	if [ -z "$token" ]; then
		case "$addr" in
		127.0.0.1:* | localhost:*) ;;
		*) warn "service listens on $addr with no LOOM_SERVICE_TOKEN — the control plane is unauthenticated" ;;
		esac
	fi

	systemctl daemon-reload
	systemctl enable --now loomd.service
	info "loomd service enabled and started (systemctl status loomd)"
}

# On a re-run/upgrade where the service already exists, just restart it to pick up
# the new binary — don't re-prompt. A fresh install prompts (or honors LOOM_SERVICE).
if [ -f "$UNIT_PATH" ] && command -v systemctl >/dev/null 2>&1 && [ "$(id -u)" -eq 0 ]; then
	info "existing loomd service found — restarting to pick up the new binary"
	systemctl daemon-reload
	systemctl restart loomd.service 2>/dev/null || warn "could not restart loomd.service"
elif want_service; then
	install_service
fi

# --- verify + PATH hint ---
case ":$PATH:" in
*":$BINDIR:"*) ;;
*) warn "$BINDIR is not on your PATH — add it, e.g.: export PATH=\"$BINDIR:\$PATH\"" ;;
esac
info "installed to $BINDIR:"
for b in "${BINS[@]}"; do printf '    %s\n' "$BINDIR/$b"; done
"$BINDIR/loom" --version 2>/dev/null | head -1 || true

# AF_XDP readiness note: the released loomd has the AF_XDP datapath built in, but
# using it (datapath: afxdp) needs root and a reasonably recent kernel.
case " ${BINS[*]} " in
*" loomd "*)
	kver="$(uname -r 2>/dev/null)"
	kmaj="${kver%%.*}"
	if [ "${kmaj:-0}" -lt 5 ] 2>/dev/null; then
		warn "loomd includes AF_XDP, but this kernel ($kver) is old — AF_XDP needs ~5.x+. Socket datapaths (udp/tcp) work regardless."
	else
		info "loomd includes AF_XDP (datapath: afxdp) — it needs root + an XDP-capable NIC; socket datapaths need neither."
	fi
	;;
esac

info "done. Next: loom run --help   (docs: https://github.com/$REPO/tree/main/docs)"
