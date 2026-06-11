# loom — Design / Architecture RFC

**Status:** Draft for discussion · **Target platform:** Linux only (for now) ·
**Name:** `loom` *(provisional — see [Open Decisions](#13-open-decisions))*

> This is a living document, not a spec. It's the starting point for a
> conversation. Disagree in the margins, propose alternatives, rip sections out.

---

## Table of contents

1. [Vision](#1-vision)
2. [Hard requirements](#2-hard-requirements)
3. [Requirement → component map](#3-requirement--component-map)
4. [Architecture overview](#4-architecture-overview)
5. [Data plane](#5-data-plane)
6. [Decoupled logging & telemetry](#6-decoupled-logging--telemetry-hard-constraint)
7. [Measurement plane](#7-measurement-plane)
8. [Control plane & security](#8-control-plane--security)
9. [Orchestration: scenarios & timeline](#9-orchestration-scenarios--timeline)
10. [Traffic emulation](#10-traffic-emulation)
11. [Roles & topology](#11-roles--topology)
12. [Repository layout](#12-repository-layout)
13. [Open decisions](#13-open-decisions)
14. [Phasing / roadmap](#14-phasing--roadmap)
- [Appendix A — Harvest map](#appendix-a--harvest-map)
- [Appendix B — Glossary](#appendix-b--glossary)

---

## 1. Vision

A single, modern tool for generating and measuring network traffic — from a
one-line throughput test to a 100-node choreographed scenario mixing realistic
application traffic. One clean **core library**; everything else (CLI, web, API,
control plane) is a thin adapter around it.

It replaces a decade of scattered, half-finished attempts (see
[Appendix A](#appendix-a--harvest-map)) with one well-architected codebase that
finally delivers the thing all of them gestured at but none completed: accurate
generation **plus** measurement, decoupled so neither perturbs the other.

**Non-goals (for now):** Windows/macOS support; being a packet *capture*/IDS
tool; replacing a full APM stack. Linux-only lets us use AF_XDP, AF_PACKET, and
NIC hardware timestamping freely.

## 2. Hard requirements

These are the stated must-haves driving the design:

1. **iperf-esque mode** — trivial point-to-point throughput/latency test.
2. **Clean core ↔ CLI/web/API separation.**
3. **Complex traffic patterns between multiple points.**
4. **Flexibility in schedulers, packet pumps, etc.**
5. **Deep logging that is performant and decoupled** — logging must *never* be
   able to impact data rates or packet pacing.
6. **Multiple datapath/driver layers** — native OS, AF_PACKET, AF_XDP, DPDK.
7. **Detailed reporting** — both streaming and end-of-run.
8. **Dedicated control plane.**
9. **Optional security** for control-plane auth & access.
10. **Traffic emulation** — HTTPS, VoIP call, SSH session, Prometheus sender, …
11. **A fun, non-cheesy name.**
12. **Linux only for now.**
13. **Agent / controller / CLI / web roles** — e.g. agents on 100 systems, a web
    display of all flow state, a controller running the test.
14. **Timeline with overlap & random timing** — e.g. HTTP every 10–100 ms within
    a size range, an SSH session starting at +45 s running 1 m, an FTP transfer
    starting at +37 s that runs until 123 MB are sent.

## 3. Requirement → component map

| # | Requirement | Where it's satisfied |
|---|---|---|
| 1 | iperf-esque mode | CLI quick path → ephemeral local + remote agent, one flow |
| 2 | core ↔ UI separation | Hexagonal core; cli/api/web/grpc are adapters with no core logic |
| 3 | multi-point patterns | Orchestrator + Scenario DSL + tag-based endpoint selection |
| 4 | flexible schedulers/pumps | `Scheduler`, `Pump`, `Datapath`, `Generator` interfaces |
| 5 | decoupled perf logging | Hot-path lock-free ring → off-path drainer; atomic counters; drop-never-block |
| 6 | datapath layers | `Datapath` backends: `socket` / `afpacket` / `afxdp` / `dpdk` |
| 7 | streaming + final reporting | `Reporter` + sinks; interval samples + final stats engine |
| 8 | dedicated control plane | gRPC control service on its own connection, separate from data |
| 9 | optional security | mTLS + enrollment tokens + per-RPC authz, off by default |
| 10 | emulation | `Emulation` generators compiled from a behavior-script primitive |
| 11 | name | `loom` (weaving many flows into one fabric) — provisional |
| 12 | Linux only | Free use of AF_XDP/AF_PACKET, `x/sys/unix`, NIC timestamping |
| 13 | roles | agent / controller / cli / web over one core; symmetric agents |
| 14 | timeline + overlap + jitter | Timeline engine: absolute/relative/recurring triggers, bounds |

## 4. Architecture overview

Ports-and-adapters (hexagonal). The **core** is a pure Go library with no
knowledge of cobra, HTTP, gRPC transport, or any UI. Everything user-facing is an
adapter.

```
                ┌──────────── adapters (no core logic) ────────────┐
   CLI (cobra)  REST / gRPC-gateway API   Web dashboard   loomctl
                └───────────────────────┬──────────────────────────┘
                                        │  control plane (gRPC + optional mTLS/authz)
        ┌───────────────────────────────┼───────────────────────────────┐
   Controller / Orchestrator            │                    Agent (loomd) × N
   - parse Scenario + Timeline          │   Register / Caps / Configure / Arm
   - resolve endpoint selection         │   Start / Stop / StreamTelemetry
   - distribute flow specs, arm timeline│   TimeSync / Health
   - aggregate telemetry, drive reports │
        └───────────────────────────────┘
                                        │
   ╔═══════════════════════════ CORE (pure library) ═══════════════════════════╗
   ║  Orchestration:  Scenario → Timeline engine  (macro scheduling, overlap)    ║
   ║  Flow = Generator + Scheduler + Datapath + endpoints + stop-condition        ║
   ║                                                                              ║
   ║   Generator (what)     Scheduler (when/rate)    Datapath (how/where)         ║
   ║    tcp/udp/icmp/quic     token / interval /       socket / afpacket /        ║
   ║    + Emulations:         poisson / soak /         afxdp / dpdk               ║
   ║    https,voip,ssh,       trace-replay                                        ║
   ║    prometheus,ftp                                                            ║
   ║                                                                              ║
   ║  Accounting (lock-free counters → windowed rates)                            ║
   ║  Measurement (latency / OWD / jitter / loss / dup · stats engine · reflector)║
   ║  TimeSync     Telemetry pipeline     Decoupled async Log                     ║
   ╚══════════════════════════════════════════════════════════════════════════════╝
```

**Core principle:** the core never imports an adapter; adapters depend on the
core. A `Flow` can be driven entirely in-process by a unit test with no network,
no CLI, no gRPC.

## 5. Data plane

A **Flow** is the atomic unit of traffic: a `Generator` (what) paced by a
`Scheduler` (when) pushed through a `Datapath` (how/where) between endpoints,
until a stop condition. Four pluggable seams:

### 5.1 `Datapath` — the packet-I/O backend ("driver/firmware layer")

The proper term for this is the **datapath** or **packet-I/O backend**;
kernel-bypass options are AF_XDP and DPDK, while AF_PACKET is an in-kernel raw
ring.

| Backend | Mechanism | Use case | Build |
|---|---|---|---|
| `socket` | kernel net stack (`net.Conn`/`PacketConn`) | default, L4 flows, portable | pure Go |
| `afpacket` | AF_PACKET v3 (TPACKET ring) | raw L2/L3, moderate rate | pure Go (`x/sys`) |
| `afxdp` | AF_XDP + XDP/eBPF, zero-copy | high pps, line-rate | build tag |
| `dpdk` | kernel-bypass PMD | extreme rate, dedicated NIC | cgo + build tag |

Each backend advertises a **capability set** — raw L2? hardware timestamping?
rate offload? max pps? — so the orchestrator can pick/validate a datapath per
flow and fail fast when a scenario asks for something a node can't do.

```go
type Datapath interface {
    Open(cfg DatapathConfig) error
    Caps() Capabilities
    TxBatch(pkts [][]byte) (sent int, err error) // hot path — no alloc/lock/log
    RxBatch(into [][]byte) (n int, err error)
    Close() error
}
```

### 5.2 `Scheduler` — intra-flow pacing / rate control

Controls inter-packet timing and rate *within* a flow (distinct from the
[Timeline](#9-orchestration-scenarios--timeline), which schedules *between*
flows).

- `token` — token-bucket rate limit
- `interval` — fixed inter-packet interval (hybrid sleep-then-spin for sub-ms)
- `poisson` / `bursty` — statistical arrival models
- `soak` — flat-out, unlimited
- `replay` — reproduce inter-packet gaps from a captured trace

```go
type Scheduler interface {
    Name() string
    // Pace blocks until the next packet should be sent; returns false to stop.
    Pace(ctx context.Context) bool
}
```

### 5.3 `Generator` — what the traffic *is*

Produces payload bytes and per-flow behavior. Raw: `tcp`, `udp`, `icmp`, `quic`.
Higher-level: the [emulations](#10-traffic-emulation). Payload sources include
patterned (sequence-numbered, for loss/reorder detection), random, and cyclic
(De Bruijn, for offset diagnostics).

```go
type Generator interface {
    Name() string
    NextPayload(buf []byte) (n int, done bool) // ring-style, no alloc on hot path
}
```

### 5.4 `Pump` — the inner loop

Binds `Generator → Scheduler → Datapath`. The **only** place where performance is
sacred. Inner TX loop does zero allocation, zero locks, zero syscalls beyond the
send, and never touches the logger directly (see §6). Separate TX and RX pumps.

**Extensibility:** Generators, Schedulers, and Datapaths register into a
**registry** (not a `switch`), so a new protocol or backend drops in without
touching existing code — the thing every prior project promised and none
delivered.

## 6. Decoupled logging & telemetry (hard constraint)

> *Logging and metrics must never be able to impact data rates or packet pacing.*

This is a first-class design constraint, not an afterthought. Rules for anything
on the data-plane hot path:

- **No allocation, no locks, no syscalls (besides the send), no formatting** in
  the inner pump loop. Buffers are preallocated and reused; no `interface{}`
  boxing in the inner loop.
- **Counters are atomics**, sharded per-worker (per-CPU where it matters). A
  separate **sampler** goroutine reads them out-of-band to compute windowed
  rates — measurement can't stall TX.
- **Log/event emission from the data plane** writes fixed-size records into a
  **per-worker lock-free SPSC ring**. A separate **drainer** goroutine (or a
  dedicated, pinned OS thread) formats and ships them. **Ring full → increment a
  `dropped` counter and move on. Never block the producer.**
- **Diagnostic/control logging** (outside the hot path) uses an async, leveled,
  structured logger (zerolog/zap style). Hot-path code emits to the ring, never
  to that logger.
- **Pacing isolation:** pump goroutines are `LockOSThread`-pinned, use the
  monotonic clock, and pace via hybrid sleep-then-spin for sub-ms accuracy. GC
  pressure is kept off the hot path (buffer pools, `GOGC` tuning, arenas where
  warranted). Telemetry export is batched, rate-limited, and on its own
  goroutine.

The litmus test: **turning logging from off to maximally verbose must not move
the achieved packet rate or pacing distribution.** This will be a benchmark gate,
not a hope.

## 7. Measurement plane

Measurement is co-equal with generation — and equally decoupled (it samples
atomics; it doesn't intercept the hot path).

- **Throughput** — native byte/packet accounting → windowed rate. **No iperf
  dependency.** Counter → 1 s sampler → sliding window → current/avg/peak rate.
- **Latency / loss / jitter / dup / reorder** — active probes + a stats engine
  (min/max/avg/stddev/coefficient-of-variation, loss %, duplicate & reorder
  detection, inter-packet spacing).
- **One-way delay (optional)** — NIC hardware timestamping (TX via socket error
  queue, RX via `SO_TIMESTAMPING` cmsg), correlated across nodes via TimeSync /
  PHC. Behind a datapath capability flag; not required for the common case.
- **TimeSync** — software clock sync (NTP-style four-timestamp handshake) for
  coordinated timelines and RTT; hardware path when OWD is requested.

### Reporting

A `Reporter` with multiple **sinks**, fed from the telemetry pipeline (so
reporting can't stall the data plane):

- **Streaming:** per-flow + aggregate interval reports during the run (to CLI,
  web, and any subscribed sink).
- **End-of-run:** full statistical summary, per-flow and aggregate.
- **Sinks:** human stdout, JSON, file, Prometheus endpoint, raw socket/stream.

## 8. Control plane & security

A **dedicated** gRPC control service, on its own connection/port, never sharing
sockets or NICs with the data plane.

**RPCs:** `Register`, `Capabilities`, `Configure(flow)`, `Arm`, `Start`, `Stop`,
`Destroy`, `StreamTelemetry`, `Health`, `TimeSync`.

Agents are **symmetric** — any agent can be the client or the server/reflector
side of a given flow, decided per-flow by the controller.

**Optional security** (off in lab, on in production; pluggable so it's genuinely
optional):

- mTLS between controller ↔ agents
- agent enrollment via a join token
- per-RPC authorization / RBAC
- audit logging of control actions

## 9. Orchestration: scenarios & timeline

Two clearly-separated scheduling levels:

- **Scheduler** (§5.2) — *intra-flow* pacing.
- **Timeline** — *inter-flow* conductor: when flows start/stop, recurrence,
  jitter, and overlap.

A **Scenario** (YAML) declares endpoints, defaults, and a timeline of events.
This is where requirement 14 lives. The user's three example flows, encoded:

```yaml
scenario: branch-office-mix
endpoints:
  - { name: client, tags: [vm, 10g, linux] }
  - { name: edge,   tags: [server, 40g]   }

defaults: { datapath: socket }          # per-flow override allowed

timeline:
  # HTTP every 10–100 ms (random), object 100K–3M, whole run, overlapping
  - name: web
    flow:   { kind: https-browse, object_size: 100KB..3MB }
    from:   client
    to:     edge
    start:  0s
    repeat: { interval: 10ms..100ms, jitter: uniform }
    stop:   end-of-test

  # SSH session starting 45 s in, runs for 1 minute
  - name: admin-ssh
    flow:  { kind: ssh-session }
    from:  client
    to:    edge
    start: +45s
    stop:  { after: 1m }

  # FTP transfer starting 37 s in, bounded by volume (123 MB), not time
  - name: backup
    flow:  { kind: ftp-transfer }
    from:  client
    to:    edge
    start: +37s
    stop:  { volume: 123MB }
```

**Trigger types:** `at` / `+offset` (absolute / relative), `repeat { interval:
range, jitter }` (recurring with randomness), `for N` / `every N`.
**Stop conditions:** `after: duration` · `volume: bytes` · `count: packets` ·
`end-of-test`. Overlap is implicit — each event spawns independent flows.

**Endpoint selection:** `oneOf` / `allOf` modes + tag expressions, e.g.
`from: tags(all(10g, win))`, `to: tags(any(40g, 10g))`, with client≠server
exclusion so a node never talks to itself by accident.

## 10. Traffic emulation

Emulations are `Generator`s that model an application's *traffic shape*, compiled
from one shared **behavior-script primitive** — a sequence of
`(direction, size-distribution, think-time-distribution, transport)` steps — so
they share machinery rather than each being bespoke.

| Emulation | Shape |
|---|---|
| `https-browse` | TLS handshake + GET bursts of object-size dist with think-time gaps (page → assets), keep-alive |
| `voip-call` | bidirectional CBR UDP (e.g. G.711: 160 B / 20 ms), duration-bound; jitter/loss measured |
| `ssh-session` | TCP, small interactive bursts with human inter-key timing + optional bulk (scp) |
| `prometheus-sender` | periodic remote-write POSTs of synthetic metric batches at scrape interval |
| `ftp-transfer` | control + data channel, volume-bound bulk transfer |

New emulations are added by describing a behavior script, not by writing a new
transport.

## 11. Roles & topology

One core, four roles:

- **Agent (`loomd`)** — runs on each node (think 100 systems). Executes flows,
  exposes datapaths/capabilities, streams telemetry. Symmetric, headless,
  minimal dependencies.
- **Controller / Orchestrator** — owns a Scenario, drives N agents, arms the
  timeline, aggregates results.
- **CLI (`loom`)** — drives the controller; **or** runs the iperf-esque quick
  test directly (ephemeral local agent + one remote, streaming + summary, no
  scenario file).
- **Web / API** — live dashboard of all flows/agents + a REST / gRPC-gateway API;
  read-mostly, talks to the controller.

**iperf-esque mode** is just the degenerate case of the full system: one
controller-less command spins an ephemeral local agent + a remote agent, runs a
single flow, and prints streaming + summary.

## 12. Repository layout

Linux-only, monorepo:

```
loom/
  cmd/
    loom/        # CLI adapter (cobra)
    loomd/       # agent daemon
    loomctl/     # controller / orchestrator   (or fold into a --role flag)
    loomweb/     # web/dashboard server
  core/
    datapath/{socket,afpacket,afxdp,dpdk}/   # afxdp, dpdk behind build tags
    pump/
    scheduler/
    generator/{tcp,udp,icmp,quic,emul}/
    flow/
    accounting/
    measure/{latency,owd,stats,reflector}/
    timesync/
    telemetry/   # decoupled event pipeline
    log/         # decoupled async logging
    plan/        # scenario + timeline engine
  control/       # gRPC service + auth providers
  api/           # REST / gRPC-gateway
  web/           # frontend
  proto/
```

Build tags isolate heavy/cgo backends (`dpdk`, `afxdp`) so the **default build is
pure Go**. A single `--role` binary is viable, but separate binaries keep the
agent small.

## 13. Open decisions

These are the forks worth settling before/while we build. **Add your take inline.**

1. **Name.** `loom` (provisional) vs **Conflux** (streams converging) /
   **Sluice** (gated flow) / **Weft** (the woven thread). Repo renames are one
   command.
2. **MVP primary datapath.** Start with `socket`; how early do we want `afxdp`
   given the kernel-bypass emphasis?
3. **Binary topology.** One `--role` binary vs separate `loom` / `loomd` /
   `loomctl`.
4. **One-way delay / HW timestamping** — must-have from day one (changes how
   central TimeSync + datapath capabilities are), or a later phase?
5. **License.** (BSD-2? consistent with prior repos.)
6. **Config surface.** YAML only, or also a Go builder API / HCL?
7. **Telemetry transport.** Reuse the control-plane gRPC stream, or a separate
   telemetry channel so it never competes with control RPCs?

## 14. Phasing / roadmap

1. **MVP / iperf-esque** — core + `socket` datapath + token/soak schedulers +
   tcp/udp generators + accounting + latency + streaming/summary reporter +
   single CLI command.
2. **Distributed** — control plane + agent/controller + TimeSync + Scenario /
   Timeline engine + multi-point selection.
3. **Emulations + datapaths** — https/voip/ssh/prom/ftp; `afpacket` → `afxdp`;
   web dashboard.
4. **Advanced** — one-way delay + HW timestamping; DPDK; control-plane security;
   trace-replay scheduler.

---

## Appendix A — Harvest map

This project consolidates ~35 prior repos. Rather than port wholesale, we lift
specific working pieces. (`HARVEST` = take the code; `REFERENCE` = port the
design; `FRESH` = build new.) File:line pointers are into the source repos as
audited.

| Component | Action | Source |
|---|---|---|
| Rate/pacing schedulers (token/interval/soak) | HARVEST | `blaster` `internal/schedulers/*` |
| Payload generators (patterned/random/De Bruijn) | HARVEST | `bperf` `payloader/*` |
| Latency probe | HARVEST | `nperfmon` `pkg/pinger/pinger.go` |
| Stats engine (avg/stddev/CoV/loss/jitter/dup) | HARVEST | `NetworkPerformanceAnalyzer` `netalyzer.go:260-434` |
| `Transceiver`/datapath base + AF_XDP/Conn backends | HARVEST | `packet` `internal/controller/transceiver.go`, `internal/transceiver/{xdp,conn}.go` |
| HW-timestamp TX path (Go) | HARVEST | `basicHWTimestamps` `timestamp_runner.go:12-163` |
| Flow/endpoint/orchestrator interfaces + `{Type,Params}` YAML | REFERENCE | `traffic` `network/endpoint`, `core/*`, `configuration/types` |
| TCP data path + dynamic HTTP webserver | HARVEST | `traffic` `network/endpoint/tcpEndpoint.go`, `network/webserver/` |
| TCP/UDP unification (`ConnEndpoint`) | REFERENCE | `anapp` `shared/network/connEndpoint.go` |
| Control-plane lifecycle proto (Setup→Start→Stop, states) | REFERENCE | `anapp` `control.proto` / `bperf` `control/*` |
| 4-timestamp reflector + traffic-profile presets | REFERENCE | `NetworkPerformanceAnalyzer` `udp*.go` |
| Remote-agent poll→fan-in pipeline | REFERENCE | `perspective` `collector/core/sensor.go` |
| RX HW-timestamp cmsg recipe | REFERENCE | `quantify` `src/UdpReceiver.cpp:48-139` |
| Software TimeSync (clock offset/delay) | HARVEST | `tgams` `internal/timesync/*` (already working) |
| Sub-ms sleep-then-spin pacing | REFERENCE | `goping` `main.go:83-91` |
| Throughput accounting (counter→sampler→window) | FRESH | concept ← `anapp` `shared/accounting/accountant.go` |
| `Collector` interface | FRESH | none of the existing ones fit |
| Plugin/command-registry surface (optional) | REFERENCE | `conductor-plugin-udpsender` `main.go` |

**Explicitly excluded:** `go-iperf`, `go-libiperf`, `nperfmon` `Speeder`
(iperf-bound — out of scope); `netmark-agent` (use `gopsutil` directly);
`ethercomms` (use `mdlayher/raw`); hand-rolled packet codecs in `PacketCraft`
(use `google/gopacket`).

**Anti-patterns observed across the old repos to design out:** `log.Fatal` inside
libraries/connection handlers, busy-wait spin loops on the hot path, stringly-typed
`map[string]string` params, missing `context`/cancellation, hardcoded
interfaces/addresses, and (in one repo) a redistributed GPL `cygwin1.dll`.

## Appendix B — Glossary

- **Datapath** — the packet-I/O backend a flow uses (kernel socket, AF_PACKET,
  AF_XDP, DPDK).
- **Flow** — one unit of traffic: generator + scheduler + datapath + endpoints +
  stop-condition.
- **Scheduler** — paces packets *within* a flow.
- **Timeline** — schedules flows *relative to each other* across a run.
- **Emulation** — a generator that mimics a real application's traffic shape.
- **Agent / Controller** — the node-local executor / the central orchestrator.
- **OWD** — one-way delay (requires synchronized clocks / HW timestamps).
- **Reflector** — echoes timestamped probe packets back for RTT/jitter/loss.
