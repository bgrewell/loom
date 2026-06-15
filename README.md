# loom

**loom** is a distributed network **traffic generation and measurement** system
for Linux. It weaves many independent traffic flows — raw protocols and realistic
application behaviors — across many machines, on a schedule you define, and
measures what happens in real time.

Think of it as *iperf when you want a quick number, and a programmable traffic
fabric when you don't*: one command for a point-to-point throughput test, or a
declarative scenario driving dozens of agents with overlapping, randomized flows.

```console
$ loom run --rate 50Mbps --duration 5s
[   1.0s]   49.98 Mbps      4459 pkts     5.95 MB
[   2.0s]   50.01 Mbps      4461 pkts     5.95 MB
...
--- summary ---
  duration : 5s
  sent     : 29.7 MB in 22300 packets
  avg rate : 49.9 Mbps
```

## Why loom

- **One tool, two modes.** A single-flow CLI for quick tests, and a control
  plane that runs multi-point scenarios across a fleet of agents.
- **Realistic, not just packet-blasting.** Raw UDP/TCP today; HTTPS/VoIP/SSH/
  Prometheus/FTP *behavior* emulation on the roadmap.
- **Fast where it counts.** A zero-copy data plane with pluggable backends —
  kernel sockets by default, **AF_XDP** for line-rate, DPDK to come — behind one
  interface, so speed is a backend choice, not a rewrite.
- **Measurement-first.** Streaming and end-of-run throughput, latency, jitter,
  and loss; one-way delay and NIC hardware timestamping are designed in.
- **Reproducible.** Seeded scenarios replay identically, so you can compare runs
  and catch regressions.

## Get started

| You are… | Start here |
|---|---|
| New to loom | **[Getting Started](docs/getting-started.md)** → **[Core Concepts](docs/concepts.md)** |
| Evaluating / an expert | **[Architecture](docs/architecture.md)** → **[Performance](docs/benchmarks.md)** |
| Deploying it | **[Deployment](docs/deployment.md)** |
| Looking something up | **[Reference](docs/reference/cli.md)** · **[Scenario schema](docs/scenario-schema.md)** |

The full manual lives in **[docs/](docs/README.md)**.

## Install

```console
curl -fsSL https://raw.githubusercontent.com/bgrewell/loom/main/scripts/install.sh | bash
```

Installs `loom`, `loomd`, and `loomctl` — prebuilt binaries when available, else
built from source. [`upgrade.sh`](scripts/upgrade.sh) and
[`uninstall.sh`](scripts/uninstall.sh) are the counterparts; tuning and options
are in [scripts/README.md](scripts/README.md).

Or with the Go toolchain:

```console
go install github.com/bgrewell/loom/cmd/loom@latest     # the CLI
go install github.com/bgrewell/loom/cmd/loomd@latest    # the agent
go install github.com/bgrewell/loom/cmd/loomctl@latest  # the controller
```

Linux only for now. See [Getting Started](docs/getting-started.md) for a guided
first run.

## Status

Active development. The architecture is settled and the single-host engine,
distributed control plane, telemetry, and an AF_XDP datapath are in; application
emulations and a web dashboard are next. Design decisions are recorded as ADRs in
[DECISIONS.md](DECISIONS.md); the plan is in [docs/roadmap.md](docs/roadmap.md).

## License

[Apache-2.0](LICENSE).
