# Getting Started

This guide takes you from nothing to a measured traffic flow in a couple of
minutes, then points you at what to read next. No prior loom knowledge assumed.

## Prerequisites

- **Linux** (loom is Linux-only for now).
- **Go 1.22+** to build from source.

## Install

```console
curl -fsSL https://raw.githubusercontent.com/bgrewell/loom/main/scripts/install.sh | bash
```

That installs all three binaries (`loom`, `loomd`, `loomctl`) — prebuilt when
available, else built from source. Options and the upgrade/uninstall counterparts
are in [scripts/README.md](https://github.com/bgrewell/loom/blob/main/scripts/README.md). Prefer the Go toolchain
directly?

```console
go install github.com/bgrewell/loom/cmd/loom@latest
```

That builds just the `loom` CLI — the single-flow tool you'll use here. (Two more
binaries, `loomd` and `loomctl`, drive multi-machine scenarios; you'll meet them
in the [multi-agent guide](guides/multi-agent-scenario.md).) If you've cloned the
repo, `go run ./cmd/loom` works too.

## Your first flow

loom can generate, pace, and **measure** traffic without anything to send to —
the `discard` datapath produces packets and accounts for them, then drops them.
That makes the very first run dependency-free:

```console
$ loom run --rate 20Mbps --duration 3s
[   1.0s]   19.98 Mbps      1785 pkts     2.38 MB
[   2.0s]   20.01 Mbps      1786 pkts     2.38 MB
[   3.0s]   19.99 Mbps      1785 pkts     2.38 MB
--- summary ---
  duration : 3s
  sent     : 7.14 MB in 5356 packets
  avg rate : 19.9 Mbps
```

What you're seeing:

- Each `[ t ]` line is a **streaming interval report** (default every 1s) — the
  live rate, packet count, and bytes for that window.
- The **summary** is the end-of-run total.

That's the whole loop: a *generator* produced packets, a *scheduler* paced them
to ~20 Mbps, a *datapath* "sent" them, and *accounting* measured it. Those four
words are the heart of loom — the [Core Concepts](concepts.md) page makes them
concrete.

## Shaping the run

A flow runs until a **stop condition** is hit. Pick whichever fits:

```console
loom run --duration 10s          # stop after time
loom run --count 50000           # stop after N packets
loom run --bytes 100MB           # stop after a volume
```

And shape *what* and *how fast*:

```console
loom run --rate 1Gbps            # target rate (empty = as fast as possible)
loom run --packet-size 9000      # jumbo frames
loom run --payload patterned     # payload content
loom run --output json           # machine-readable reports
```

`loom run --help` lists everything; the [CLI reference](reference/cli.md) explains
each flag.

## Sending somewhere real

To put packets on the wire, choose a network datapath and a target. Start a
throwaway UDP sink in one terminal and send to it from another:

```console
# terminal 1 — a UDP sink (anything that listens works)
nc -u -l 9999 >/dev/null

# terminal 2 — send 200 MB of UDP to it
loom run --datapath udp --target 127.0.0.1:9999 --bytes 200MB
```

loom has faster datapaths than the kernel socket (notably **AF_XDP**); when and
why to use them is covered in [Choosing a datapath](guides/datapaths.md).

## Going distributed

A single `loom run` is one flow on one host. The real power is running a
**scenario** — many flows, between many endpoints, on a timeline — across a fleet
of agents. That's a short next step:

- **[Run a multi-agent scenario](guides/multi-agent-scenario.md)** — start
  `loomd` agents, write a scenario file, and drive it with `loomctl`.

## Where to next

- **[Core Concepts](concepts.md)** — the mental model, so the rest of the docs
  read easily.
- **[Guides](guides/README.md)** — task-by-task walkthroughs.
- **[Architecture](architecture.md)** — if you want to know how it works inside.
