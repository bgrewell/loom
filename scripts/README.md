# scripts

Operational scripts for installing and maintaining loom.

| Script | Purpose |
|---|---|
| [`install.sh`](install.sh) | Install the `loom`, `loomd`, `loomctl` binaries. |
| [`upgrade.sh`](upgrade.sh) | Upgrade an existing install to the latest (or a pinned) version. |
| [`uninstall.sh`](uninstall.sh) | Remove the loom binaries. |
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

```console
# install just the agent, pinned, into a custom dir
curl -fsSL .../install.sh | LOOM_VERSION=v0.2.0 LOOM_PREFIX=/opt/loom/bin LOOM_BINARIES=loomd bash
```

`uninstall.sh` honors `LOOM_PREFIX`/`LOOM_BINARIES` too, and leaves any loomd
config, systemd units, and data in place.

## Notes

- The `curl | bash` URLs above require the repository (or its releases) to be
  reachable. While the repo is private, fetch the scripts with an authenticated
  client, or run them from a checkout (`bash scripts/install.sh`).
- Building from source against a private module needs Git auth and
  `GOPRIVATE=github.com/bgrewell/*`.
- Running `loomd` as a service (systemd, container) is covered in
  [docs/deployment.md](../docs/deployment.md).
