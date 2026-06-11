# loom — Design / Architecture RFC

**Status:** Draft for discussion · **Target platform:** Linux only (for now) ·
**Name:** `loom` *(settled — [ADR-0007](DECISIONS.md#adr-0007--name-loom))*

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
15. [Testing, CI/CD & performance regression](#15-testing-cicd--performance-regression)
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
| 9 | optional security | enrollment/auth token + optional mTLS, off by default — no RBAC |
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
                                        │  control plane (gRPC + optional mTLS / token auth)
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
| `memory` | in-process loopback (no kernel) | unit/integration tests, deterministic | pure Go |

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

**Optional security** — off by default for lab use, switched on for shared or
production environments. The model is deliberately simple: **authenticate the
connection, then trust it.** No role hierarchies, no per-RPC permission matrix.

- a shared **auth / enrollment token** an agent presents to join a controller, and
- optional **mTLS** for transport encryption and certificate-based identity.

Fine-grained access control (RBAC) is explicitly out of scope — it's complexity a
traffic-test fabric of agents you already own doesn't need, and it can be layered
on later if a multi-tenant shared testbed ever demands it
(see [ADR-0014](DECISIONS.md#adr-0014--simple-auth-not-rbac)).

## 9. Orchestration: scenarios & timeline

"Scheduling" means two different things in a traffic tool, and blurring them is
how earlier attempts got muddled. loom keeps them distinct:

- **Scheduler** (§5.2) — *micro*: paces packets **within** one flow (rate,
  inter-packet timing).
- **Timeline** — *macro*: choreographs flows **relative to each other** across a
  run — when each starts and stops, how often it recurs, and how they overlap.

A **Scenario** (YAML) is that macro description: the endpoints traffic runs
between, plus a timeline of events over them. It's where requirement 14 — overlap
and randomized timing — lives. The full grammar (value/distribution forms,
selectors, triggers, stop conditions, flow kinds) is in
[docs/scenario-schema.md](docs/scenario-schema.md). The three example flows from
our discussion, encoded:

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

**Triggers:** `at` / `+offset` (absolute or relative start), `repeat { interval,
jitter }` (recurring, with randomness drawn from the value grammar), `for N` /
`every N`.
**Stop conditions:** `after: <duration>` · `volume: <bytes>` · `count: <packets>`
· `end-of-test`.
**Overlap is the default, not a special case:** events are independent, so any
number of flows can be live at once — that's the entire point of a timeline.
**Endpoint selection:** `oneOf` / `allOf` / `any` modes and tag expressions
(`from: tags(all(10g, linux))`, `to: tags(any(40g, 10g))`), with automatic
client ≠ server exclusion so a node never picks itself for both ends.

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

One core library, wearing four hats. Only the **agent** ever touches the wire;
every other role coordinates.

- **Agent (`loomd`)** — runs on each node under test (think 100 systems). It is
  the only component that generates and measures traffic: it executes flows,
  advertises its datapath capabilities, and streams telemetry back. Headless,
  minimal dependencies, and **symmetric** — the controller decides per-flow
  whether a given agent is the sender or the receiver/reflector.
- **Controller** — the brain. It loads a Scenario, resolves endpoint selection,
  distributes flow specs to the relevant agents, arms and runs the timeline, and
  aggregates their telemetry. It holds no data-plane state of its own.
- **CLI (`loom`)** — the primary human interface. It drives a controller for full
  scenarios, **or** runs the iperf-esque quick test on its own (below).
- **Web / API** — a live view of every agent and flow, plus a REST / gRPC-gateway
  API for automation. Read-mostly: it observes through the controller, it doesn't
  bypass it.

**iperf-esque mode** is just the whole system collapsed to its smallest form: a
single `loom` command stands up an ephemeral local agent, talks to one remote
agent, runs one flow, and prints streaming + final numbers — no controller, no
scenario file. Same engine, fewer moving parts.

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
    datapath/{socket,afpacket,afxdp,dpdk,memory}/  # afxdp,dpdk build-tagged; memory = tests
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
  tests/         # DART integration suites (lxd + physical testbed) + baselines
  .github/workflows/   # CI pipelines
```

Build tags isolate heavy/cgo backends (`dpdk`, `afxdp`) so the **default build is
pure Go**. A single `--role` binary is viable, but separate binaries keep the
agent small.

## 13. Open decisions

Settled — recorded as ADRs in [DECISIONS.md](DECISIONS.md):

| ADR | Decision | Outcome |
|---|---|---|
| 0007 | Name | **loom** (`loom`/`loomd`/`loomctl`) |
| 0008 | MVP datapath | **socket** first; `afxdp` in phase 3 |
| 0009 | Binary topology | separate `loom`/`loomd`/`loomctl`, one module |
| 0010 | OWD / HW timestamping | later phase; TimeSync + capability seams day one |
| 0012 | Config surface | YAML + Go builder API |
| 0013 | Telemetry transport | separate channel (never competes with control) |
| 0017 | CLI framework | **stencil** |

**Still open:** ADR-0011 **License** — TBD before first public release
(BSD-2 / MIT / Apache-2.0).

## 14. Phasing / roadmap

1. **MVP / iperf-esque** — core + `socket` datapath + token/soak schedulers +
   tcp/udp generators + accounting + latency + streaming/summary reporter +
   single CLI command. Tests and CI land **with** this code, not after: unit +
   contract suites, the in-memory datapath, and the CI skeleton (§15).
2. **Distributed** — control plane + agent/controller + TimeSync + Scenario /
   Timeline engine + multi-point selection.
3. **Emulations + datapaths** — https/voip/ssh/prom/ftp; `afpacket` → `afxdp`;
   web dashboard.
4. **Advanced** — one-way delay + HW timestamping; DPDK; control-plane security;
   trace-replay scheduler.

The physical-host DART + performance tier (§15) comes online with phase 2
(multi-host) and grows as datapaths and emulations land.

---

## 15. Testing, CI/CD & performance regression

Tests and CI are **part of "done" from the first commit**, not a later phase. The
hexagonal core (§4) exists partly to make this cheap: with the datapath,
scheduler, and generator behind interfaces, the whole engine runs in-process
against an **in-memory datapath** (§5.1) — no NICs, no root, fully deterministic
via the scenario `seed`. Real hardware is exercised separately on a physical
testbed. Full detail: [docs/testing.md](docs/testing.md),
[docs/ci-cd.md](docs/ci-cd.md).

### Test tiers

1. **Unit** — table-driven (happy / error / boundary), deterministic, no network.
   Complete coverage of core *logic*; a coverage gate fails CI below threshold.
   `-race` is mandatory.
2. **Contract / conformance** — one shared suite every `Datapath`, `Scheduler`,
   and `Generator` implementation must pass, so registry plugins can't drift from
   the interface contract.
3. **Benchmarks + alloc gates** — hot-path microbenchmarks assert **0 allocs/op**
   on the pump inner loop and feed `benchstat` regression comparison. This is
   where §6 is *enforced*: "toggling logging must not move the achieved rate or
   pacing" is a benchmark, not a hope.
4. **Integration (DART)** — [DART](https://github.com/bgrewell/dart) YAML suites
   drive real `loom`/`loomd` binaries end to end. Two tiers: cheap **LXD** suites
   on cloud CI for correctness, and **physical-host** suites on the testbed for
   real NICs and rates.
5. **Performance regression** — the testbed captures throughput / latency / loss
   for known scenarios and compares against committed baselines (below).

### Performance regression detection

The reason physical-host integration exists: catch *performance* regressions, not
just *correctness* ones. The thing we want surfaced automatically:

> **tcp-100g / socket** throughput **down 28.4 %: 12,476 → 8,932 Mbps**
> (baseline 12,476 ± 5 %) — **FAIL**

- Each perf scenario has a committed **baseline** (median of N runs) per
  `(host-pair, scenario, datapath)`, with a **tolerance** band.
- A run outside tolerance fails the job and posts the delta as a PR comment.
- Results also go to a **trend store** so regressions can be bisected and
  improvements stay visible over time.
- Baselines change only via a PR that explains why — never silently (a silent
  bump hides exactly the regressions this tier exists to catch).

### CI/CD

Cloud runners: lint/vet → unit + race + coverage gate → build matrix (pure-Go
default; `afxdp`/`dpdk` build-tag compile checks) → benchmarks + benchstat →
LXD DART. **Self-hosted runners on the physical testbed** (labeled, e.g.
`loom-testbed`, `nic-100g`, `xdp-capable`) run the physical DART + performance
tier on merge/nightly/release. Required status checks gate merges to `main`.
Release builds inject version metadata (via the `stencil` dev-CLI, consistent
with the other repos) and publish the `loom` / `loomd` artifacts.

---

## Appendix A — Harvest map

This project consolidates ~35 prior repos. Rather than port wholesale, we lift
specific working pieces. (`HARVEST` = take the code; `REFERENCE` = port the
design; `FRESH` = build new.) File:line pointers are into the source repos as
audited. The process for turning these into reusable
[blueprints](docs/blueprints/) and then retiring the source repos is in
[docs/eol-plan.md](docs/eol-plan.md).

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
