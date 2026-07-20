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
**Status:** Accepted · **Date:** 2026-06-13 · **Resolved:** 2026-06-14

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
**Resolution.** Implemented: `listen bool` → `enum FlowRole` (field 20 + name
`reserved`; `role` added at 21, UNSPECIFIED treated as SENDER, REFLECTOR returns
Unimplemented); `flow.duration` → `google.protobuf.Duration`; `uint32 api_version`
added to `HealthResponse` (set to `control.APIVersion`) and `RegisterRequest`. The
`auth_token` field was **deliberately omitted**: per-RPC authentication already
rides call metadata via the ADR-0014 interceptor on every RPC (including
Register), so a message field would duplicate working machinery; a future
enrollment-token flow can add one when its semantics differ from transport auth.

## ADR-0022 — Inject component registries; functional options on constructors
**Status:** Accepted · **Date:** 2026-06-13 · **Resolved:** 2026-06-15

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
**Resolution.** Implemented as `core/components.Components` (Tx/Rx datapath +
generator + scheduler + payload registries) with `Default()`/`OrDefault()`.
`flow.Build(spec, *Components)` (nil = default) and `control.NewServer(version,
WithComponents/WithTelemetryInterval/WithMaxFlows/WithAuthToken)` take it; the
agent advertises and builds from its injected set, so `Capabilities` is
per-agent-truthful. `controller.New` gained `WithDialer` (testable without gRPC).
**Pragmatic limit kept on purpose:** the built-in and build-tagged (afxdp)
factories still self-register into the package-level registries, and `Default()`
wraps those — so `init()`-based and tag-gated plugin registration keeps working
with far less churn. Components is the injectable *handle*; the global registries
remain the default registration *sink*. (One known gap: the `stream` generator
still resolves its payloader from the global `payload.Registry` internally, so a
custom `Components.Payloads` affects capability reporting but not building until
the generator takes a payload registry — a later cleanup if needed.)

---

### Real application traffic (design: [docs/design/real-app-traffic.md](docs/design/real-app-traffic.md))

## ADR-0023 — One connection-factory seam: `netpath.Network`
**Status:** Accepted · **Date:** 2026-07-17

**Context.** Wire-true application engines (VoIP, HTTP/TLS, video) need
`net.Conn`/`net.PacketConn` semantics over injectable stacks: the kernel,
UDP-encoded-over-a-raw-L3-datapath, a userspace TCP/IP stack over a datapath, or
an in-memory test loopback. Today `core/emul/reqresp` calls concrete
`net.Dial`/`net.Listen`, so it cannot ride any injected datapath — and each new
app could grow its own ad-hoc transport abstraction (`media.Transport`, emul
`Dialer`/`Listener` funcs, …).
**Decision.** Exactly one seam: `netpath.Network`
(`DialContext`/`ListenPacket`/`Listen`/`Close`), a registry component
(`Components.Networks`) with pure-data `Options` per ADR-0006/0022; embedders
with live datapaths use direct constructors instead of the registry.
Implementations: `host` (kernel, default), `dgram` (real IPv4+UDP headers over
raw-L3 datapaths), `netstack` (gVisor, ADR-0026), `memory` (paired in-process
nets for CI). `core/emul/reqresp` is refactored onto the seam with back-compat
wrappers.
**Consequences.** Every current and future app (including the planned SIP UA)
dials/listens through one abstraction and therefore runs over any datapath —
kernel, tunnel, or memory — unchanged. Retires the reqresp untunnelable defect.
No parallel transport abstractions can accrete.

## ADR-0024 — New APP_CLIENT/APP_SERVER flow roles, not a RESPONDER selector
**Status:** Accepted · **Date:** 2026-07-17

**Context.** App engines need agent-side placement. The existing
RESPONDER/REQUESTER roles (request/response emulations) could be overloaded with
a selector param naming the app ("responder, emulation=voip"), avoiding new enum
values.
**Decision.** Add `FLOW_ROLE_APP_CLIENT = 6` / `FLOW_ROLE_APP_SERVER = 7`
(additive per ADR-0021) plus `FlowSpec.app/network/local` (fields 16–18),
dispatching to the `AppClients`/`AppServers` registries.
**Consequences.** Clean taxonomy and a natural telemetry home
(`TelemetrySample.app = 12`). The rejected alternative is recorded here
deliberately: overloading RESPONDER would (a) file wire-true protocol engines
under `core/emul`, which is documented and implemented as *shape-only* carriage
(mode.go), blurring loom's own taxonomy; (b) couple apps to reqresp's transport
field and BehaviorScript plumbing even though apps are bidirectional with their
own metrics plane; (c) risk the reflector's `Unimplemented` arm. Two additive
enum values are the cheaper long-term cost. `core/emul` shapes stay shape-only
by design.

## ADR-0025 — Voice quality via full ITU-T G.107/G.107.1 E-model, not curve fits
**Status:** Accepted · **Date:** 2026-07-17

**Context.** `core/quality/emodel` turns delay/loss/burstiness into R-factor and
MOS. Simplified approximations (e.g. the FiDO2011 curve fit) are common,
plausible, and subtly wrong — the worst failure mode for a measurement tool,
because nobody notices.
**Decision.** Implement the full G.107 default formulas (narrowband) and G.107.1
(wideband, its own 0..129 R scale and R→MOS map): computed Ro/Is (not
constants), `Id = Idte + Idle + Idd` with the 100 ms Idd knee, `Ie,eff` with
Gilbert `BurstR`, explicit `ComposeTa` (network OWD + jitter-buffer nominal +
codec frame/lookahead delay) and Ppl-includes-discards semantics. No curve fits
anywhere. Golden tests pin the G.107 Table 4 verification examples and
zero-impairment R = 93.2 ± 0.01; every result carries a `Components`
(Ro/Is/Idte/Idle/Idd/Ie,eff) audit breakdown; live runs are cross-checked
against Wireshark RTP stream analysis. Opus impairment rows are provisional
non-ITU values, flagged and overridable via `codec.Register`.
**Consequences.** More math up front, but auditable, referenceable numbers — a
disputed MOS can be decomposed term by term against the spec. The same
discipline (spec-exact, golden-tested) applies to the RFC 3550 Appendix A
receiver statistics feeding it.

## ADR-0026 — gVisor isolated in `core/netstack` behind a build tag
**Status:** Accepted · **Date:** 2026-07-17

**Context.** TCP-based apps (HTTP/TLS, video) over a raw-L3 datapath need a
userspace TCP/IP stack. gVisor's `pkg/tcpip` is the proven pure-Go option (no
NET_ADMIN/TUN/netns), but it is a large module with internal API churn, and
per-stack memory would hurt at fleet scale.
**Decision.** Wrap gVisor in one package, `core/netstack`, pinned at a tested
release; all gVisor imports stay inside it. A `loom_nonetstack` build tag stubs
it for minimal agents (same isolation pattern as the heavy datapaths, ADR-0008).
One multi-address `Stack` hosts many local addresses with per-connection
source-bound `Network(local)` views — never one stack per address. The
`stack.LinkEndpoint` is implemented directly over the ADR-0019 frame contract
(`TxReserve`/`TxCommit`, `RxPoll`/`RxRelease`), avoiding a `channel.Endpoint`
copy per packet. UDP apps ride the lightweight `dgram` network, so fleet voice
never pays gVisor cost.
**Consequences.** The heavy dependency is swappable/stubbable and its blast
radius is one package. A netstack-vs-kernel benchmark delta and a sender-side
timestamp audit are published before any TCP-derived measurement is claimed, so
userspace-stack behavior is quantified rather than silently attributed to the
network under test.

## ADR-0027 — One-way delay is always labeled with method + error bound
**Status:** Accepted · **Date:** 2026-07-17

**Context.** OWD feeds the E-model's delay impairment (Id). Software clock sync
has real error, and asymmetric paths (data through a tunnel, control over a
management LAN) can bias offsets — an unlabeled OWD number is a lie waiting to
happen.
**Decision.** `core/owd` exposes `Estimate{Value, ErrBound, Method}` and an
`OffsetProvider` seam. Three tiers, always labeled end-to-end (proto, CLI,
Prometheus): **timesync** (TimeSync exchanges over a symmetric path, never the
path under test; `owd.Tracker` min-delay-filters per window and drift-fits),
**rtt/2** (ErrBound = RTT/2, never presented as measured), **assume-synced**
(operator asserts NTP/PTP with a declared max error). When ErrBound exceeds a
threshold, E-model input clamps to the labeled rtt/2 tier. Builds on the
ADR-0010 TimeSync seam; hardware timestamps later fill `Frame.Meta` (ADR-0020)
with no API change.
**Consequences.** Every OWD-derived number (including MOS) carries honest
uncertainty; downstream consumers can propagate the error bar instead of
trusting a point estimate. Slightly wider telemetry rows (`owd_err_ms`,
`owd_method`).
