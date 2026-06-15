# Core Concepts

A small vocabulary unlocks the whole system. This page defines it once, plainly,
so every other page reads easily. Skim the bold terms; come back when one shows
up elsewhere.

## The big picture

loom has two layers:

- a **data plane** — the per-flow machinery that actually makes and measures
  packets, and
- a **control plane** — the coordination that decides *which* flows run *where*
  and *when*.

A single `loom run` uses only the data plane. A distributed scenario adds the
control plane on top. Same engine underneath.

## Data plane

A **flow** is loom's atomic unit of traffic: one stream of packets from a source
to a destination, with a stop condition. A flow is assembled from four pluggable
parts:

| Part | What it decides | Examples |
|---|---|---|
| **Generator** | what bytes each packet carries | `stream` |
| **Payload** | the content the generator fills with | `random`, `patterned` |
| **Scheduler** | *when* the next packet(s) may go (pacing/rate) | `soak` (max rate), `interval` (paced) |
| **Datapath** | how packets get on (and off) the wire | `discard`, `memory`, `udp`, `tcp`, `afxdp` |

The **pump** is the inner loop that ties them together: it asks the scheduler how
many packets may go now, has the generator fill that many **frames** from the
datapath, hands them back to the datapath to send, and records the result in
**accounting** (running byte/packet/rate counters). The pump is allocation-free
on the hot path — that's a hard design rule, not an aspiration.

A **frame** is one packet's buffer, *owned by the datapath*. The generator writes
straight into it and the datapath sends it without copying — so a zero-copy
backend like AF_XDP never copies packet bytes. (See
[Architecture](architecture.md) for the ownership contract.)

A **stop condition** ends a flow: after a duration, a packet count, or a byte
volume — whichever comes first. No stop condition means "run until told to stop."

> **For experts.** Each part is a small interface resolved from a registry, so a
> new scheduler/payload/datapath drops in without touching the pump
> ([ADR-0006](../DECISIONS.md)). The datapath interface is split into transmit
> (`TxDatapath`) and receive (`RxDatapath`) sides and is batch- and zero-copy-
> capable so AF_XDP/DPDK slot in unchanged ([ADR-0019/0020](../DECISIONS.md)).

## Datapaths, briefly

The datapath is where speed lives. All of them present the same interface; you
choose by name:

- **`discard`** — generates and accounts, drops on send. No receiver needed;
  great for rate tests and the default for `loom run`.
- **`memory`** — in-process loopback for tests (zero-copy).
- **`udp` / `tcp`** — kernel sockets. The portable default for real traffic.
- **`afxdp`** — kernel-bypass, zero-copy, near line-rate. Needs a NIC and root;
  built into `loomd` with a build tag. See
  [Choosing a datapath](guides/datapaths.md).

## Control plane

To run flows across machines, two roles cooperate:

- **Agent (`loomd`)** — the worker. It runs on each host that should send or
  receive traffic, executes the flows it's told to, and streams back telemetry.
  It is the only component that touches the wire.
- **Controller (`loomctl`)** — the brain for a run. It reads a scenario, figures
  out which endpoints each flow runs between, and tells the agents what to do
  over a gRPC **control plane**.

They talk over a dedicated control channel; the actual traffic (the **data
plane**) flows agent-to-agent, never through the controller.

### Scenarios

A **scenario** is a declarative file describing *what traffic runs, between which
points, when, and for how long*. Its pieces:

- **Endpoints** — the named points traffic runs between (e.g. `client`,
  `server`). Each maps to an agent and can carry **tags** (`10g`, `linux`) and,
  for NIC-bound datapaths, an `iface`/`queue`.
- **Timeline** — a list of **events**. Each event says: this *flow*, from these
  endpoints *to* those endpoints, starting at this time, with this stop
  condition — optionally repeating with randomized intervals.
- **Selectors** — how an event picks endpoints: by name, by tag expression, or
  `oneOf`/`allOf`/`any` of a set. The controller resolves a selector to concrete
  agents at fire time (and never picks the same endpoint as both ends).
- **Seed** — make the randomness (intervals, selections) reproducible, so a run
  replays identically for comparison.

The full grammar is in the **[scenario schema](scenario-schema.md)**; the
**[multi-agent guide](guides/multi-agent-scenario.md)** walks through a real one.

### Roles

Within a scenario, each event becomes two flows: a **sender** on the source agent
and a **receiver** on the destination agent. The controller wires them together
(the receiver binds first and reports where to send) and starts both.

### Telemetry

While a run is live, agents stream per-flow **telemetry** (bytes, packets, rate)
back to the controller on a channel separate from the control RPCs, so high-rate
metrics never slow down coordination. The controller aggregates them into a fleet
view that **observers** render — the CLI prints it; a dashboard/API is just
another observer.

## How it fits together

```
            loomctl (controller)            you write a scenario;
              │  control plane (gRPC)        loomctl drives the agents
      ┌───────┴────────┐
   loomd (agent)    loomd (agent)            each agent runs flows:
   ┌──────────┐     ┌──────────┐               generator → scheduler →
   │  pump →   │====▶│ receiver │  data plane    datapath → accounting
   │  datapath │     │          │  (agent ↔ agent, not via the controller)
   └──────────┘     └──────────┘
        └── telemetry ──▶ loomctl ──▶ observers (CLI / dashboard)
```

Next: **[Guides](guides/README.md)** to do something with these pieces, or
**[Architecture](architecture.md)** for how they're built.
