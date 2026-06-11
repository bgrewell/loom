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

## ADR-0011 — License (still open)
**Status:** Proposed · **Date:** 2026-06-10

**TBD** — to be decided before the first public code/release. Prior repos lean
BSD-2; MIT and Apache-2.0 (explicit patent grant) are the other candidates. This
is the **one remaining open ADR.**

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
