# Deployment

How to run loom for real: agents on the hosts that carry traffic, a controller to
drive them, with the right permissions and a sane security posture. For the
threat model and tokens, read [Securing the control plane](guides/securing-the-control-plane.md)
alongside this.

## The pieces in production

- **`loomd`** runs as a long-lived service on every host that should send or
  receive traffic. It's the only component that touches the wire.
- **`loomctl`** is invoked per run (from an operator's machine or a CI job) to
  drive a scenario. It needs network reach to each agent's control port; it does
  **not** sit in the data path.

## Running an agent

`loomd` is configured by environment variables:

| Variable | Default | Purpose |
|---|---|---|
| `LOOMD_ADDR` | `127.0.0.1:9551` | control-plane listen address; set to `:9551` (or a specific IP) to expose it |
| `LOOMD_TOKEN` | _(unset)_ | shared auth token required on every RPC; **set this whenever you bind a routable address** |
| `LOOMD_TELEMETRY` | `1s` | telemetry sample interval (a Go duration) |

A minimal systemd unit:

```ini
# /etc/systemd/system/loomd.service
[Unit]
Description=loom agent
After=network-online.target

[Service]
Environment=LOOMD_ADDR=:9551
Environment=LOOMD_TOKEN=%S/loom/token        # or an EnvironmentFile / secret
ExecStart=/usr/local/bin/loomd
Restart=on-failure
# For the AF_XDP datapath (kernel-bypass), grant the needed capabilities:
AmbientCapabilities=CAP_NET_RAW CAP_BPF
# Otherwise the default (kernel sockets) needs no extra privileges.

[Install]
WantedBy=multi-user.target
```

In a container, run `loomd` as the entrypoint, publish the control port, and pass
the same env. For the AF_XDP datapath the container needs `CAP_NET_RAW` +
`CAP_BPF` (and a NIC it can drive); for kernel sockets it needs neither.

## Permissions by datapath

| Datapath | Privileges | Notes |
|---|---|---|
| `discard` / `memory` | none | no wire access at all |
| `udp` / `tcp` | none | ordinary kernel sockets |
| `afxdp` | root or `CAP_NET_RAW`+`CAP_BPF` | also a `loomd` built with `-tags afxdp`; see [Choosing a datapath](guides/datapaths.md) |

A stock `loomd` is pure-Go and needs no special build; an AF_XDP-capable agent is
built with `go build -tags afxdp ./cmd/loomd`. A default agent simply rejects
`datapath: afxdp` with `InvalidArgument`.

## Running a controller

`loomctl run` maps endpoint names to agent addresses and drives the scenario:

```console
export LOOM_TOKEN="$(cat /etc/loom/token)"   # if agents require a token
loomctl run \
  -f scenario.yaml \
  -a 'client=10.0.0.11:9551,server=10.0.0.12:9551' \
  --horizon 60s --output json > run.jsonl
```

`--output json` makes the live aggregate machine-readable (one object per
interval) for capture or a dashboard. The controller exits when the timeline
finishes or `--horizon` elapses, tearing down the flows it placed.

## Security posture (recap)

- Default loopback bind; expose only with `LOOMD_ADDR` **and** `LOOMD_TOKEN`.
- The token is a secret — distribute it with config management, never in scenario
  files or version control.
- Agents bound to a routable address without a token start but warn; treat that
  as trusted-network-only.
- mTLS and per-agent identity are designed but not yet implemented — keep exposed
  control planes on a management network.

Full rationale: [Securing the control plane](guides/securing-the-control-plane.md)
and [ADR-0014](../DECISIONS.md).

## Fleets

Pre-installing agents across a whole datacenter so any project can test between
any hosts — discovery, inventory, enrollment at scale — is an active design topic,
not yet built. The direction (a lightweight coordinator agents self-register into,
with per-project controllers querying it by tag) is being worked out; today,
point `loomctl` at the agent addresses you want directly.
