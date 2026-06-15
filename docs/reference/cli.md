# CLI reference

The three commands and every flag/environment variable they take. Run any with
`--help` for the same list inline.

- [`loom`](#loom) — single-flow generator + measurement (one host).
- [`loomd`](#loomd) — the agent (worker) for distributed runs.
- [`loomctl`](#loomctl) — the controller that drives a scenario across agents.

---

## `loom`

Build one flow from flags, run it, and report. Uses only the data plane — no
agents required.

### `loom run`

| Flag | Default | Meaning |
|---|---|---|
| `-g, --generator` | `stream` | traffic generator |
| `--payload` | `random` | payload source: `random` \| `patterned` |
| `-d, --datapath` | `discard` | `discard` \| `udp` \| `tcp` \| `memory` |
| `-t, --target` | _(empty)_ | destination `host:port` (for `udp`/`tcp`) |
| `-s, --packet-size` | `1400` | packet size in bytes |
| `-r, --rate` | _(empty)_ | send rate, e.g. `100Mbps` (empty = as fast as possible) |
| `--duration` | `10s` | stop after this much time |
| `--count` | `0` | stop after N packets (`0` = off) |
| `--bytes` | _(empty)_ | stop after a volume, e.g. `100MB` (empty = off) |
| `-i, --interval` | `1s` | streaming report interval |
| `-o, --output` | `human` | report format: `human` \| `json` |

Stop conditions combine — whichever is reached first ends the run; with none set
(and no `--rate` ceiling) it runs for `--duration`. Example:

```console
loom run --datapath udp --target 10.0.0.2:9999 --rate 1Gbps --bytes 5GB --output json
```

JSON output is one object per line: `{"type":"sample",…}` per interval and a
final `{"type":"summary",…}`.

---

## `loomd`

The agent. Configured entirely by environment variables (no flags); it serves the
control plane until stopped (SIGINT for a graceful shutdown).

| Variable | Default | Meaning |
|---|---|---|
| `LOOMD_ADDR` | `127.0.0.1:9551` | control-plane listen address; set `:9551` or an IP to expose off-host |
| `LOOMD_TOKEN` | _(unset)_ | shared auth token required on every RPC ([security](../guides/securing-the-control-plane.md)) |
| `LOOMD_TELEMETRY` | `1s` | telemetry sample interval (Go duration) |

```console
LOOMD_ADDR=:9551 LOOMD_TOKEN=s3cret loomd
```

The AF_XDP datapath additionally requires a `loomd` built with `-tags afxdp` and
`CAP_NET_RAW`+`CAP_BPF` (or root). See [Deployment](../deployment.md).

---

## `loomctl`

The controller. Reads a scenario and drives it across agents.

### `loomctl run`

| Flag | Default | Meaning |
|---|---|---|
| `-f, --scenario` | _(required)_ | scenario YAML file |
| `-a, --agent` | _(none)_ | `endpoint=host:port` pairs, comma-separated |
| `--horizon` | `30s` | upper bound on the run; an end-of-test scenario stops here |
| `-l, --live` | `true` | stream live aggregate telemetry |
| `-p, --per-flow` | `false` | show per-flow throughput (live and in the summary) |
| `-i, --interval` | `1s` | telemetry interval |
| `-o, --output` | `human` | telemetry format: `human` \| `json` |
| `-t, --token` | `$LOOM_TOKEN` | control-plane auth token |

The run stops as soon as its bounded flows finish (so a 10s flow takes ~10s, not
the full `--horizon`); `--horizon` only caps an unbounded `end-of-test` scenario.
It ends with a summary (tx/rx totals and average throughput); `--per-flow` adds a
line per flow.

```console
loomctl run -f scenario.yaml \
  -a 'client=10.0.0.11:9551,server=10.0.0.12:9551' \
  --horizon 60s --output json
```

The `-a` value maps each scenario **endpoint** name to an agent's control
address. The scenario file grammar is in the
[scenario schema](../scenario-schema.md).

---

See also: [Scenario schema](../scenario-schema.md) ·
[Choosing a datapath](../guides/datapaths.md) · [Performance](../benchmarks.md).
