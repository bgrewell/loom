# Architecture

This page explains how loom is built and *why*. It assumes you've skimmed
[Core Concepts](concepts.md). For the exhaustive design and the rationale behind
each call, see **[DESIGN.md](../DESIGN.md)** and the ADRs in
**[DECISIONS.md](../DECISIONS.md)**, which this page links into.

## Design philosophy

loom is a **hexagonal (ports-and-adapters)** system. A pure `core/` library holds
all the logic — flows, pumps, datapaths, schedulers, stats — and knows nothing
about transports or UIs. Thin adapters wrap it: the `loom` CLI, the `loomd`
agent, the `loomctl` controller, and (later) a web/API layer. The core imports no
adapter; adapters depend on the core, never the reverse ([ADR-0003](../DECISIONS.md)).

Three rules shape everything:

1. **The hot path is sacred.** The per-packet loop allocates nothing, takes no
   locks, and never blocks on logging ([ADR-0005](../DECISIONS.md)). Speed is a
   property you can't bolt on later, so it's protected by tests from day one.
2. **Mechanism is pluggable; the core is not.** Datapaths, schedulers,
   generators, and payloads are small interfaces resolved from registries, so new
   ones drop in without a `switch` and without touching the engine
   ([ADR-0006](../DECISIONS.md)).
3. **Measurement is first-class.** Accounting and telemetry are part of the
   engine, not an afterthought, and are decoupled so they never perturb the
   traffic they measure.

### Repository layout

```
core/        the pure library (no transport/UI deps)
  pump/        the allocation-free inner loop
  datapath/    packet I/O backends (discard/memory/udp/tcp/afxdp) + the frame interface
  generator/   what bytes go in each packet
  scheduler/   intra-flow pacing / rate control
  payload/     packet content sources
  flow/        a flow = generator+scheduler+datapath+stop; and the receiver
  accounting/  lock-free byte/packet/rate counters
  components/  the injectable registry bundle (DI)
  scenario/    scenario/timeline file model + parser
  timeline/    turns a scenario into a schedule of "fires"
  selection/   endpoint selection (tags, oneOf/allOf/any)
  stats/ latency/ timesync/ units/ log/   measurement + helpers
control/     the gRPC control-plane service (agent side) + client
controller/  drives a scenario across agents (loomctl's engine)
cmd/         loom (CLI) · loomd (agent) · loomctl (controller)
proto/ api/  the loom.v1 wire contract and generated code
```

## The data plane

### The pump loop

The **pump** is the engine. One iteration:

1. ask the **scheduler** how many packets may be sent now (`Pace(ctx, max)`),
2. **reserve** that many frames from the datapath,
3. have the **generator** fill each frame,
4. **commit** the batch to the datapath, and
5. record the sent frames in **accounting**.

A rate scheduler returns `1` per gap (strict pacing); an unpaced `soak` scheduler
returns the full batch, so a max-rate flow amortizes the per-commit syscall
across many packets ([batched pacing](../DECISIONS.md)). Nothing in the loop
allocates — a benchmark gate fails the build if that ever changes.

### Frames and zero-copy

The datapath interface is **batch-first and zero-copy-capable** — this is what
lets AF_XDP and DPDK reach line rate without a rewrite ([ADR-0019](../DECISIONS.md)).
It is split by direction:

- **`TxDatapath`** — `TxReserve(n)` hands back datapath-owned **frames**; the
  caller fills them and `TxCommit` sends the batch.
- **`RxDatapath`** — `RxPoll(max)` returns received frames; `RxRelease` returns
  them to the backend.

A **frame**'s buffer may *alias* device memory (an AF_XDP UMEM frame, a DPDK
mempool buffer). The borrow contract: a frame is valid only until the matching
`TxCommit`/`RxRelease`, and consumers must not retain it — so a backend hands out
slices of its rings without copying packet bytes. Accounting only keeps counts
and timestamps, never payload, so it honors the contract for free. Each RX frame
also carries per-packet **metadata** (`Nanos`, source) — the seam for one-way
delay and hardware timestamping ([ADR-0020](../DECISIONS.md)).

The in-process `arena` backend is a zero-copy loopback that *proves* the contract
(a received packet is read from the exact memory it was sent from); the
kernel-socket backends use a shared frame pool; AF_XDP implements it directly over
UMEM rings.

### Decoupled logging & telemetry

Logging must never slow the data plane. The hot path emits events into a
lock-free single-producer ring; a separate drainer consumes them off the critical
path. If the ring is full, events are dropped and counted — the producer never
blocks ([ADR-0005](../DECISIONS.md), DESIGN §6). The benchmark suite asserts both
the zero-allocation property and that turning logging on doesn't move the rate.

## The control plane

For distributed runs, `loomd` (agent) and `loomctl` (controller) speak the
**`loom.v1`** gRPC service ([control.proto](../proto/loom/v1/control.proto)). It
carries coordination only — traffic never flows over it.

- **Lifecycle.** `Configure` builds a flow and (for receivers) returns an
  ephemeral data port; `Start`/`Stop`/`Destroy` manage it; `Health`,
  `Capabilities`, and `Register` handle discovery. The agent's flow manager is
  goroutine-safe and contains a panicking flow to that flow, never the process.
- **Roles.** A `FlowSpec.role` selects sender vs receiver (an enum, so it can
  grow to reflector/bidirectional). The controller places a receiver, learns
  where to send, then starts the matching sender.
- **TimeSync.** A four-timestamp NTP-style exchange estimates clock offset and
  delay between controller and agent — the basis for one-way-delay measurement
  ([ADR-0010](../DECISIONS.md)).
- **Wire discipline.** Field numbers are never reused, removed fields are
  reserved, closed sets are enums, and a `api_version` rides the handshake so
  peers detect a mismatch ([ADR-0021](../DECISIONS.md)).

### Telemetry transport

Agents stream per-flow samples back on a **separate channel** from the control
RPCs, so high-rate metrics never contend with coordination ([ADR-0013](../DECISIONS.md)).
The controller aggregates them and pushes snapshots to **observers**; the CLI is
one observer, a future dashboard/API is just another.

## Extensibility & dependency injection

Datapaths/generators/schedulers/payloads self-register into per-kind registries.
Those are bundled into an injectable **`Components`** value: production uses
`components.Default()` (the registered set), and the agent or a test can inject
its own — so `Capabilities` reports exactly what *that* agent offers, not whatever
happens to be linked ([ADR-0022](../DECISIONS.md)). Constructors use functional
options (`control.NewServer(version, WithAuthToken(…), WithComponents(…))`),
matching the project's design standards.

To add a backend you implement the interface, register a factory, and satisfy the
shared **contract test** for its kind — no engine changes. See
[Contributing](contributing.md).

## Security posture

The control plane is **secure-by-default-for-localhost**: `loomd` binds
`127.0.0.1` unless you opt into a routable address, and supports an optional
shared **auth token** (a constant-time-checked bearer token on every RPC); bind
it to a network without a token and it warns loudly ([ADR-0014](../DECISIONS.md)).
Optional mTLS and fleet enrollment are designed but not yet built. An agent is a
remotely-aimable traffic generator, so input is bounded (packet-size and
max-flow caps) to keep a hostile or buggy controller from exhausting it. See
[Deployment](deployment.md) for operating guidance.

## Performance model

The engine generates at roughly **17 M packets/s per core** (≈170 Gbps at 1400 B,
~500 Gbps with jumbo frames) — it is essentially never the bottleneck. Real
throughput is set by the **datapath**: a kernel UDP socket does one syscall per
packet (~2 Gbps single-stream), while AF_XDP commits a batch per syscall and
approaches NIC line rate. Aggregate scales ~linearly with independent flows
across cores. Numbers, methodology, and caveats are in
[Performance](benchmarks.md).
