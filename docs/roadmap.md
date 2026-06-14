# loom roadmap

From the phase-1 scaffold to feature-complete. **Phases are milestones**; each has
a demoable Definition of Done and a task breakdown (→ GitHub issues). Tracks the
hard requirements in [DESIGN.md §2](../DESIGN.md#2-hard-requirements) and the
[blueprints](blueprints/).

## Current state

Phase-1 **scaffold** merged: core interfaces + `soak`/`interval` schedulers,
`random`/`patterned` payloaders, lock-free accounting, `memory` + UDP-`socket`
datapaths, the `loom` CLI on stencil, CI + DART smoke. The rest of phase 1 below
makes it actually push and measure traffic.

---

## Phase 1 — Single-host engine (iperf-esque MVP)

**DoD:** `loom run` drives a tcp/udp flow between local endpoints — paced,
accounted, with latency/loss stats and streaming + end-of-run reports — in one
command, no scenario file.

- [ ] **Registry** for datapath/scheduler/generator/payload — pluggable, no `switch`
- [ ] **Generator** interface + **TCP generator**
- [ ] **UDP generator**
- [ ] **Pump** — the alloc-free inner loop (generator → scheduler → datapath → accounting)
- [ ] **Flow** — bind components + **stop conditions** (duration/volume/count); Run/Stop
- [ ] **Decoupled logging** — async logger + per-worker hot-path ring + drainer (drop-never-block, §6)
- [ ] **Hot-path benchmarks** — 0-allocs/op gate + the logging-invariant benchmark (§6)
- [ ] **Latency probe** (icmp/udp) + typed results — blueprint: latency-probe
- [ ] **Stats engine** — avg/min/max/stddev/CoV/loss/jitter/dup — blueprint: stats-engine
- [ ] **Reporter** — interface + stdout (human/json), streaming + summary
- [ ] **Rate/size/duration parsing** (reuse `go-conversions`)
- [ ] **`loom run`** CLI command — the iperf-esque single-flow path
- [ ] **Contract suites** — Datapath/Scheduler/Generator/Payloader conformance
- [ ] **DART single-host** integration suite

## Phase 2 — Distributed control plane

**DoD:** a controller runs a scenario file across 2+ agents — flows between
selected endpoints on a timeline with overlap and random timing.

- [ ] **Control proto + gRPC service** — Register/Caps/Configure/Arm/Start/Stop/Destroy/StreamTelemetry/Health/TimeSync
- [ ] **loomd agent** — execute flows, advertise capabilities, stream telemetry
- [ ] **loomctl controller** — load scenario, distribute flows, arm timeline, aggregate
- [ ] **TimeSync** — software clock sync across agents
- [ ] **Scenario parser** — YAML → model ([scenario schema](scenario-schema.md))
- [ ] **Timeline engine** — triggers (at/+offset/repeat+jitter), stop conditions, overlap, seeded
- [ ] **Endpoint selection** — tags, oneOf/allOf/any, client ≠ server
- [ ] **Ephemeral data-port negotiation** — blueprint: control-plane
- [ ] **Telemetry transport** — separate channel (ADR-0013)
- [ ] **Aggregate reporter** — across agents, streaming to the controller
- [ ] **DART multi-node LXD** suite

## Phase 3 — Emulations, faster datapaths, web

**DoD:** realistic app emulations run over a scenario; the AF_XDP datapath is
available; a web dashboard shows live flow state.

- [ ] **Behavior-script primitive** (emulation engine) — blueprint: emulation
- [ ] **https-browse** + **dynamic-webserver** — blueprint: dynamic-webserver
- [ ] **voip-call** (CBR UDP)
- [ ] **ssh-session**
- [ ] **prometheus-sender**
- [ ] **ftp-transfer**
- [x] **Batch-first datapath interface** + per-packet RX metadata — done; the
      seam is ready for a native AF_XDP backend with no interface change
      ([ADR-0019](../DECISIONS.md#adr-0019--batch-first-datapath-interface),
      [ADR-0020](../DECISIONS.md#adr-0020--per-packet-rx-metadata-carrier))
- [ ] **AF_PACKET datapath**
- [ ] **AF_XDP datapath** + capability model — blueprint: datapath-backends
- [ ] **Reporter sinks** — file/json, prometheus, socket
- [x] **Wire/proto discipline** — reserved ranges, FlowRole enum, api_version
      handshake, protobuf.Duration ([ADR-0021](../DECISIONS.md#adr-0021--wireproto-evolution-discipline)); auth rides call metadata (ADR-0014)
- [ ] **Component DI + functional-option constructors** ([ADR-0022](../DECISIONS.md#adr-0022--inject-component-registries-functional-options-on-constructors))
- [ ] **Web dashboard** + REST/gRPC-gateway API
- [x] **Optional security** — shared auth token (ADR-0014): `LOOMD_TOKEN` on the
      agent, `--token`/`$LOOM_TOKEN` on loomctl; loomd defaults to loopback and
      warns when bound routable without a token. mTLS + enrollment still to come.
- [ ] **DART physical-host** suite + perf baselines (needs [dart#41](https://github.com/bgrewell/dart/issues/41))

## Phase 4 — Advanced & hardening

**DoD:** one-way delay via NIC hardware timestamps; DPDK; perf-regression gating
on the physical testbed; release-ready.

- [ ] **HW timestamping** (TX/RX) — blueprint: hw-timestamping
- [ ] **One-way-delay correlation** (PHC/TimeSync)
- [ ] **DPDK datapath** (cgo, build tag)
- [ ] **Schedulers** — poisson/bursty + trace-replay
- [ ] **Perf-regression CI** on the testbed (baselines + tolerance)
- [ ] **Release engineering** — goreleaser, `loomd` systemd unit, container image
- [ ] **Hardening** — coverage gate, Apache-header audit, docs/usage polish

---

## Cross-cutting (every phase)

- Tests land **with** code — unit + contract + DART; the §6 decoupled-logging
  invariant stays a benchmark gate.
- Decisions recorded in [DECISIONS.md](../DECISIONS.md); design changes reflected
  in [DESIGN.md](../DESIGN.md).
- Issues for a phase are created as that phase is started; this doc is the source
  of truth for scope.
