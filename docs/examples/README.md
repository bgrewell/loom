# loom example scenarios

A curated set of ready-to-run scenario files covering loom's datapaths (TCP, UDP,
AF_XDP), application-traffic emulations, and timeline features. Copy one, tweak the
knobs, and run.

Installed copies (from the install/upgrade scripts) live in:

- `/usr/share/loom/examples/` for a system-wide (root) install, or
- `~/.local/share/loom/examples/` for a user install.

## Running an example

These scenarios run between two hosts — a **client** and a **server**, each
running a `loomd` agent. Map each endpoint name to its agent's control address
with `-a`:

```sh
loomctl run -f tcp-throughput.scenario.yaml \
  -a 'client=10.0.0.1:9551,server=10.0.0.2:9551' --live
```

New to loom? Start with `quickstart.scenario.yaml` — it's the one example meant to
run on a single machine (two agents on localhost) to confirm your setup works:

```sh
LOOMD_ADDR=:9551 loomd &   LOOMD_ADDR=:9552 loomd &
loomctl run -f quickstart.scenario.yaml \
  -a 'client=127.0.0.1:9551,server=127.0.0.1:9552' --live
```

Useful flags: `--live` (stream consolidated interval lines), `--per-flow` (break
each line down per flow), `--interval` (reporting cadence), `--horizon` (max run
time). The end-of-run **summary is authoritative**.

## What's here

### Quickstart
| File | Demonstrates |
|---|---|
| `quickstart.scenario.yaml` | minimal proof-of-life flow (runs on localhost) |

### TCP (`datapath: tcp`)
| File | Demonstrates |
|---|---|
| `tcp-throughput.scenario.yaml` | one stream for a fixed time (≈ `iperf3 -c`) |
| `tcp-bulk-transfer.scenario.yaml` | move a fixed volume (10 GB), then stop |
| `tcp-bidirectional.scenario.yaml` | simultaneous up + down (full duplex) |
| `tcp-parallel-streams.scenario.yaml` | four concurrent streams (≈ `iperf3 -P 4`) |
| `tcp-rate-limited.scenario.yaml` | pace a stream to a target rate |

### UDP (`datapath: udp`)
| File | Demonstrates |
|---|---|
| `udp-throughput.scenario.yaml` | unpaced flood (max throughput, shows loss) |
| `udp-rate-limited.scenario.yaml` | constant-bit-rate at a target rate |
| `udp-jumbo.scenario.yaml` | large 8900-byte (jumbo) datagrams |
| `udp-bidirectional.scenario.yaml` | simultaneous CBR up + down |

### AF_XDP (`datapath: afxdp`, kernel bypass — needs root + an XDP NIC)
| File | Demonstrates |
|---|---|
| `afxdp-veth.scenario.yaml` | a flow over a local veth pair (one host) |
| `afxdp-nic-to-nic.scenario.yaml` | a flow over real NICs across two hosts |

### Application emulations (traffic *shapes*)
| File | Demonstrates |
|---|---|
| `emul-https-browse.scenario.yaml` | request/response browsing session (TCP) |
| `emul-voip-call.scenario.yaml` | constant-bit-rate RTP-style audio (UDP) |
| `emul-ssh-session.scenario.yaml` | interactive keystrokes + an scp (TCP) |
| `emul-prometheus-remote-write.scenario.yaml` | periodic metrics batches (TCP) |

### Advanced (timeline features)
| File | Demonstrates |
|---|---|
| `advanced-mixed-timeline.scenario.yaml` | overlapping flows starting at different offsets |
| `advanced-repeating-bursts.scenario.yaml` | a burst that recurs on an interval (`repeat`) |

## Key knobs

- **`datapath`** (event level): `tcp` | `udp` | `afxdp`. It can also be set inside
  the `flow:` block. Omitted ⇒ `udp`.
- **`flow.kind`**: `tcp`/`udp` for raw traffic, or an emulation name
  (`https-browse`, `voip-call`, `ssh-session`, `prometheus-sender`).
- **`flow.packet_size`**: datagram size for UDP/AF_XDP. For TCP it's ignored — TCP
  is a byte stream and loom writes in large blocks regardless.
- **`flow.rate`**: e.g. `1Gbps`, `500Mbps`. Omitted ⇒ unpaced (soak).
- **`stop`**: `{after: 10s}` | `{volume: 10GB}` | `{count: 1000000}` | `end-of-test`.

See [`../scenario-schema.md`](../scenario-schema.md) for the full grammar.
