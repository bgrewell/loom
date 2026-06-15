# scripts

Operational scripts for installing and maintaining loom.

| Script | Purpose |
|---|---|
| [`install.sh`](install.sh) | Install the `loom`, `loomd`, `loomctl` binaries (optionally as a systemd service). |
| [`upgrade.sh`](upgrade.sh) | Upgrade an existing install to the latest (or a pinned) version. |
| [`uninstall.sh`](uninstall.sh) | Remove the loom binaries and the loomd service. |
| [`loomd.service`](loomd.service) | Reference systemd unit for the agent (the installer generates one for you). |
| [`gen-proto.sh`](gen-proto.sh) | Regenerate the gRPC code from the protos. |

## One-liners

```console
# install (latest)
curl -fsSL https://raw.githubusercontent.com/bgrewell/loom/main/scripts/install.sh | bash

# upgrade
curl -fsSL https://raw.githubusercontent.com/bgrewell/loom/main/scripts/upgrade.sh | bash

# uninstall
curl -fsSL https://raw.githubusercontent.com/bgrewell/loom/main/scripts/uninstall.sh | bash
```

Prefer to read before you run? Download, inspect, then execute — same as any
`curl | bash`.

## How install works

It installs **prebuilt release binaries** when a release exists for your
platform, and otherwise **builds from source** with `go install` (needs Go). It
is Linux-only (amd64/arm64) and idempotent.

Tune it with environment variables:

| Variable | Default | Effect |
|---|---|---|
| `LOOM_VERSION` | `latest` | version/tag to install (e.g. `v0.2.0`) |
| `LOOM_PREFIX` | `/usr/local/bin` (root) else `~/.local/bin` | install directory |
| `LOOM_BINARIES` | `loom loomd loomctl` | subset to install |
| `LOOM_SERVICE` | _(prompt)_ | install loomd as a systemd service: `1`/`yes` to install, `0`/`no` to skip. Unset prompts on a terminal and skips when piped. |
| `LOOM_SERVICE_ADDR` | `:9551` | `LOOMD_ADDR` for the service |
| `LOOM_SERVICE_TOKEN` | _(none)_ | `LOOMD_TOKEN` for the service |

```console
# install just the agent, pinned, into a custom dir
curl -fsSL .../install.sh | LOOM_VERSION=v0.2.0 LOOM_PREFIX=/opt/loom/bin LOOM_BINARIES=loomd bash

# install and start loomd as a systemd service, non-interactively (no `loomd &`)
curl -fsSL .../install.sh | sudo LOOM_SERVICE=1 LOOM_SERVICE_TOKEN=s3cret bash
```

### loomd as a service

When run as root on a systemd host, `install.sh` offers to install
`/etc/systemd/system/loomd.service`, then `enable --now`s it — so the agent
starts on boot and you skip the `loomd &` dance. On a terminal it prompts; piped
(`curl | bash`) it skips unless `LOOM_SERVICE=1`. Manage it the usual way:

```console
systemctl status loomd        # health
journalctl -u loomd -f        # logs
systemctl restart loomd       # after editing the unit's Environment=
```

`upgrade.sh` restarts an existing service automatically to pick up the new
binary. `uninstall.sh` (as root) stops, disables, and removes the unit; it
honors `LOOM_PREFIX`/`LOOM_BINARIES` and leaves any config/data in place. A
reference unit is in [`loomd.service`](loomd.service).

## Notes

- Releases are cut by [GoReleaser](../.goreleaser.yaml) on each `vX.Y.Z` tag
  (`.github/workflows/release.yml`), publishing the
  `loom_<version>_linux_<arch>.tar.gz` assets `install.sh` downloads. Before the
  first release, `install.sh` builds from source (needs Go).
- Running `loomd` as a service (systemd, container) is covered in
  [docs/deployment.md](../docs/deployment.md).
