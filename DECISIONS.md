# Decisions (ADR log)

Lightweight architecture decision records. One entry per decision. Keep them
short. When a decision changes, add a new entry that supersedes the old one
rather than editing history.

**Status values:** `Proposed` (open, needs a call) · `Accepted` (settled) ·
`Superseded by ADR-NNNN`.

> Open items here mirror [DESIGN.md §13](DESIGN.md#13-open-decisions). The forks
> we haven't called yet are recorded as `Proposed` so the discussion has a home.

---

## ADR-0001 — Consolidate prior projects into one tool
**Status:** Accepted · **Date:** 2026-06-10

**Context.** ~35 prior repos (tgams, traffic, blaster, bperf, nperfmon, packet,
…) each implement a slice of "generate + measure network traffic," none complete.
**Decision.** Build one new tool (`loom`) and harvest the working pieces (see
[DESIGN.md Appendix A](DESIGN.md#appendix-a--harvest-map)); archive the rest later.
**Consequences.** A clean break, not a fork of any single repo. Old repos stay as
reference until the harvest is done.

## ADR-0002 — Linux only (for now)
**Status:** Accepted · **Date:** 2026-06-10

**Context.** Kernel-bypass (AF_XDP/DPDK), AF_PACKET, and NIC hardware
timestamping are central and Linux-specific.
**Decision.** Target Linux only initially. Don't carry cross-platform abstractions
we won't exercise.
**Consequences.** Free use of `x/sys/unix`, AF_XDP/AF_PACKET, `SO_TIMESTAMPING`.
Revisit portability only if there's demand.

## ADR-0003 — Hexagonal core; UI is adapters
**Status:** Accepted · **Date:** 2026-06-10

**Context.** Requirement: clean separation of core from CLI/web/API/control.
**Decision.** A pure Go core library with zero adapter dependencies; CLI, web,
API, and control transport are adapters that depend on the core, never the
reverse.
**Consequences.** Core is unit-testable in-process with no network/UI. Adapters
stay thin.

## ADR-0004 — Native generation; no iperf dependency
**Status:** Accepted · **Date:** 2026-06-10

**Context.** Several prior tools wrap iperf3. We want full control of pacing,
payloads, and accounting, and an iperf-esque mode is just one preset.
**Decision.** Generate traffic natively. Do **not** depend on or wrap iperf3.
Exclude `go-iperf`, `go-libiperf`, and the iperf-based `nperfmon` Speeder.
**Consequences.** Throughput must come from native byte accounting (build fresh).
The "iperf-esque mode" is a CLI preset over the native engine, not a shell-out.

## ADR-0005 — Decoupled hot-path logging/telemetry is a hard constraint
**Status:** Accepted · **Date:** 2026-06-10

**Context.** Logging or metrics must never perturb data rates or packet pacing.
**Decision.** Hot path does no alloc/lock/syscall/format; counters are atomics
read out-of-band; data-plane events go to per-worker lock-free rings drained
off-path; ring-full drops and counts, never blocks. Enforced by a benchmark gate:
toggling logging must not move achieved rate or pacing distribution.
**Consequences.** More upfront engineering (buffer pools, pinned pump goroutines).
A standing benchmark guards the invariant.

## ADR-0006 — Pluggable datapath/scheduler/generator via a registry
**Status:** Accepted · **Date:** 2026-06-10

**Context.** Every prior tool used a `switch` to add protocols/backends, so
"add one without touching existing code" never held.
**Decision.** `Datapath`, `Scheduler`, and `Generator` register into a registry;
no central `switch`.
**Consequences.** New protocols/backends are additive. Slightly more registration
boilerplate; worth it.

---

## Resolved (were open) — and the one still open

## ADR-0007 — Name: `loom`
**Status:** Accepted · **Date:** 2026-06-11

`loom` (weaving many traffic threads into one fabric). Binaries: `loom` (CLI),
`loomd` (agent), `loomctl` (controller); module path `github.com/bgrewell/loom`.
Alternatives considered: Conflux, Sluice, Weft.

## ADR-0008 — MVP primary datapath: `socket`, `afxdp` in phase 3
**Status:** Accepted · **Date:** 2026-06-11

The MVP ships the `socket` (kernel net stack) datapath; `afxdp` arrives in phase 3.
The `memory` test backend exists from day one. The datapath capability model lets
a scenario opt into a faster backend once available.

## ADR-0009 — Binary topology: separate binaries, one module
**Status:** Accepted · **Date:** 2026-06-11

Separate `loom` / `loomd` / `loomctl` binaries over one shared core module —
keeps the agent (`loomd`) small for fleet deployment. A combined dev build is
optional, not the shipping shape.

## ADR-0010 — One-way delay / HW timestamping: later phase, seams now
**Status:** Accepted · **Date:** 2026-06-11

OWD + NIC hardware timestamping land in phase 4, but the **TimeSync** service and
the **datapath capability model** are built from day one so adding OWD is not a
retrofit. Software RTT/jitter ships earlier.

## ADR-0011 — License: Apache-2.0
**Status:** Accepted · **Date:** 2026-06-11

**Apache-2.0** — chosen for its explicit **patent grant** and patent-retaliation
clause, which matter in a patent-dense domain (networking) and once the project
takes outside contributions. BSD-2 and MIT were the alternatives (simpler, but no
patent protection). `LICENSE` is in the repo; source files will carry the standard
Apache header once code lands.

## ADR-0012 — Config surface: YAML + Go builder API
**Status:** Accepted · **Date:** 2026-06-11

YAML scenarios are the primary surface (see
[scenario schema](docs/scenario-schema.md)); a **Go builder API** is also exposed
for programmatic scenarios (tests, embedding). HCL is not pursued.

## ADR-0013 — Telemetry transport: separate channel
**Status:** Accepted · **Date:** 2026-06-11

Telemetry streams over a **separate channel**, not multiplexed onto the
control-plane RPCs, so high-rate telemetry can never compete with or stall control
traffic.

---

### Added after the initial draft

## ADR-0014 — Simple auth, not RBAC
**Status:** Accepted · **Date:** 2026-06-10

**Context.** §8 originally floated per-RPC authorization / RBAC for the control
plane. For a traffic-test fabric — infrastructure you own, driving agents you
deployed — role-based access control is complexity without a matching need.
**Decision.** Keep control-plane security simple: an optional shared
auth/enrollment token plus optional mTLS for transport encryption and identity.
Authenticate the connection, then trust it. No roles, no per-RPC permission
matrix.
**Consequences.** Much less to build and operate. Not foreclosed — if a genuine
multi-tenant shared testbed ever needs per-team isolation, RBAC can be layered on
then. Supersedes the RBAC bullet that was in DESIGN.md §8.

## ADR-0015 — Tests and CI from the first commit
**Status:** Accepted · **Date:** 2026-06-10

**Context.** Prior projects bolted tests on late (or never), so regressions and
half-built subsystems hid for years.
**Decision.** Five test tiers ship with the code from day one — unit
(table-driven, coverage-gated), interface contract/conformance suites, benchmarks
with hot-path alloc gates, and [DART](https://github.com/bgrewell/dart)
integration suites (LXD + physical). A first-class in-memory datapath makes the
full engine unit-testable without NICs. CI runs the gate on every PR. See
[docs/testing.md](docs/testing.md).
**Consequences.** Slower very first commits; far cheaper everything after.
Testability constrains the architecture (interfaces, injected clocks/RNG) — which
we wanted anyway.

## ADR-0016 — Performance regression gating on a physical testbed
**Status:** Accepted · **Date:** 2026-06-10

**Context.** A traffic tool's correctness includes its *numbers*; a 28 %
throughput drop is a regression even when every functional test still passes.
**Decision.** Self-hosted runners on physical hosts run real-NIC DART suites,
capture throughput/latency/loss, and compare against committed baselines (median +
tolerance) per `(host-pair, scenario, datapath)`. Outside tolerance = CI failure +
a PR comment with the delta; results also go to a trend store for bisecting.
Baselines change only via an explaining PR. See [docs/ci-cd.md](docs/ci-cd.md).
**Consequences.** Needs a maintained testbed and self-hosted runners. Catches the
exact class of regression (12,476 → 8,932 Mbps) that unit/LXD tests can't.

## ADR-0017 — CLI built on `stencil`
**Status:** Accepted · **Date:** 2026-06-11

**Context.** The CLI adapter needs a command/flag framework.
`github.com/bgrewell/stencil` is the house framework (used by sshwiz, testbox,
smart-gateway), has a Claude skill, and provides the build-time version injection
the release flow (ADR-0016) already assumes.
**Decision.** Build the `loom` CLI on `stencil` and use its dev-CLI for
version/build metadata.
**Consequences.** Ecosystem consistency; one fewer per-repo decision.

## ADR-0018 — Extract to blueprints, then archive (don't delete) the old repos
**Status:** Accepted · **Date:** 2026-06-10

**Context.** ~30 prior traffic/measurement repos hold scattered useful ideas but
are superseded by loom. We don't want to lose the knowledge or keep 30 dead repos
cluttering the account.
**Decision.** Extract the useful ideas into loom **blueprints** and **snippets**
first; then `gh repo archive` each source repo (read-only, with an EOL notice in
its README pointing to loom). Delete only local-only scratch with nothing worth
keeping. Keep active tooling (`dart`, `stencil`, `go-conversions`,
`claude-skills`). The iperf wrappers (`go-iperf`/`go-libiperf`) and the
WAN-emulation cluster are **out of scope and left untouched**. Full process and
disposition in [docs/eol-plan.md](docs/eol-plan.md).
**Consequences.** Knowledge is preserved independent of the archived repos;
harvest-map `file:line` links keep resolving; git history is retained; the
account gets a clean read-only attic instead of a graveyard.

## ADR-0019 — Batch-first datapath interface
**Status:** Accepted · **Date:** 2026-06-13 · **Resolved:** 2026-06-14

**Context.** [DESIGN.md §5.1](DESIGN.md#51-datapath--the-packet-io-backend-driverfirmware-layer)
specifies a batch datapath (`TxBatch(pkts [][]byte)` / `RxBatch(into [][]byte)`),
but the Phase-1/2 implementation shipped a single-packet `Send([]byte)` /
`Recv([]byte)` interface as the MVP. The whole point of the planned AF_XDP/DPDK
backends (ADR-0008, Phase 3) is amortizing per-syscall/per-ring cost across a
batch; a per-packet interface cannot express that, and `Memory.Send` already
allocates+copies per call — the opposite of the zero-copy goal. Changing the
interface after four backends, the contract suite, the pump, and the receiver
exist is exactly the breaking churn ADR-0006 ("switch-free, additive") was meant
to avoid.
**Decision.** Move `Datapath` to batch-first (`TxBatch`/`RxBatch`) **before** the
first real NIC backend lands, while there are no external consumers. Existing
single-packet backends (discard/memory/udp/tcp) get a `singlePacket` adapter that
loops internally, so the migration is mechanical. Define the **buffer-ownership
contract** at the same time — caller-fills-then-flushes vs a pool
`Reserve(n) [][]byte` + `Flush()` — because AF_XDP fills frames the kernel owns
and zero-copy needs an explicit fill/flush/return-to-pool model. The pump and
`Receiver` consume the batch interface (looping over a batch of 1 is fine until a
backend benefits).
**Consequences.** One deliberate interface change now instead of a forced one
later; the hot path can be made genuinely alloc/lock/log-free per ADR-0005. Costs
a small amount of adapter boilerplate for the trivial backends.
**Resolution (Model B, datapath-owned).** Ownership is the **datapath's**:
`TxReserve(n)`/`TxCommit` and `RxPoll(max)`/`RxRelease` hand out `Frame`s whose
`Data` aliases backend memory, valid only until the matching commit/release (the
borrow contract). Kernel-socket backends use a shared `framePool`; the in-process
`arena` is a zero-copy loopback that proves a received packet is read from the
exact memory it was sent from. Interfaces split into `TxDatapath`/`RxDatapath`.
Delivered across two PRs (interface + adapters, then native backends).

## ADR-0020 — Per-packet RX metadata carrier
**Status:** Accepted · **Date:** 2026-06-13 · **Resolved:** 2026-06-14

**Context.** ADR-0010 keeps one-way-delay / hardware-timestamping for a later
phase but commits to having the *seams* in place now so adding OWD isn't a
retrofit. `Capabilities.HardwareTimestamps` exists, but `Recv([]byte) (int, error)`
returns only a length — there is nowhere to surface an RX timestamp, a TX
completion timestamp, or the source address (the receiver currently drops the
peer addr). Loss/reorder detection (patterned payload + sequence) likewise has no
place to read the sequence/timestamp back. Adding any of these later changes the
datapath signature — a breaking change.
**Decision.** Introduce an `RxMeta` (or `Packet`) value carrying `{buf, n,
rxTimestamp, srcAddr}` and have the RX path return it (e.g. `RxBatch(into []Packet)`,
composing with ADR-0019). Populate only `Nanos` (software timestamp) initially;
hardware timestamps fill the same field later with no signature change. This is
the data-carrying counterpart to the capability flag.
**Consequences.** OWD, jitter, and loss/reorder become additive features on a
stable interface. Slightly larger RX value type.
**Resolution.** Implemented as `datapath.Frame.Meta` (`Meta{Nanos, Src}`), carried
on every `RxPoll` frame. The UDP listener stamps `Nanos` from the software clock
and fills `Src` from the datagram; NIC hardware timestamps will populate the same
field later with no signature change.

## ADR-0021 — Wire/proto evolution discipline
**Status:** Proposed · **Date:** 2026-06-13

**Context.** The control plane is `loom.v1` (good), but the proto has gaps that
are cheap to fix now and expensive after any consumer pins the wire: `listen` is
a `bool` (can't grow to reflector/echo/bidirectional roles); no `reserved` ranges
protect removed/renumbered fields; `Register`/`Health` carry no
protocol/api-version field and no place for the ADR-0014 auth token; flow
`duration` is a stringly-typed field re-parsed on the agent.
**Decision.** Adopt wire discipline: (1) add `reserved` ranges to every message as
fields evolve, starting now; (2) replace `listen bool` with
`enum FlowRole { SENDER; RECEIVER; REFLECTOR; }`; (3) add `uint32 api_version` /
`string protocol_version` to the handshake and an optional `string auth_token` to
`RegisterRequest`; (4) use `google.protobuf.Duration` for durations instead of
strings. Field-number gaps stay reserved, never reused.
**Consequences.** The wire can evolve without breaking old agents/controllers,
and auth/versioning have a home. One-time regen of `api/loomv1` and small
agent/controller edits (the bool→enum touch is the only non-additive bit, done
now while loom is the sole consumer).

## ADR-0022 — Inject component registries; functional options on constructors
**Status:** Proposed · **Date:** 2026-06-13

**Context.** Datapath/generator/scheduler/payload are exposed as package-level
`var Registry = registry.New[…]()` populated in `init()`. The generic
`Registry[T,O]` type is sound and thread-safe, but the *global singleton* usage is
hidden global mutable state (counter to the project's standards): registries can't
be varied per-agent or per-test, two agents in one process share one set, and
`Capabilities` reports whatever happens to be linked rather than what an agent is
configured to allow. Separately, `control.NewServer(version)` accretes setters
(`SetTelemetryInterval`, `SetMaxFlows`, `SetAuthToken`) and `flow.Build(spec)`
hardcodes registry lookups — both are constructors-with-many-optionals that the
project's own standard says should use functional options.
**Decision.** Introduce a `Components` struct holding the four registries, injected
into `flow.Build(components, spec)` and `control.NewServer`, with
`DefaultComponents()` performing today's registration so the default path is
unchanged. Convert the agent/server/controller constructors to functional options
(`control.NewServer(version, WithTelemetryInterval(d), WithComponents(c),
WithAuthToken(t))`) and inject the dialer into `controller.New` so it is testable
without real gRPC.
**Consequences.** Truthful per-agent capability reporting, per-test component
sets, no global mutable state, and constructors that match the house style. A
mechanical refactor across `flow`/`control`/`controller`; the global registries
can remain as the `DefaultComponents()` backing during transition.
