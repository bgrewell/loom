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

## Open (Proposed) — need a call

## ADR-0007 — Name
**Status:** Proposed · **Date:** 2026-06-10

`loom` (weaving flows into a fabric) is the working name. Alternatives:
**Conflux** (streams converging), **Sluice** (gated flow), **Weft** (the woven
thread). Repo rename is one command (`gh repo rename`). **Decision pending.**

## ADR-0008 — MVP primary datapath
**Status:** Proposed · **Date:** 2026-06-10

Start with `socket` (kernel net stack) for the MVP. Open question: how early to
add `afxdp` given the kernel-bypass emphasis — phase 1 or phase 3? **Pending.**

## ADR-0009 — Binary topology
**Status:** Proposed · **Date:** 2026-06-10

One `--role` binary vs separate `loom` / `loomd` / `loomctl`. Trade-off: single
binary is simpler to ship; separate keeps the agent tiny. **Pending.**

## ADR-0010 — One-way delay / HW timestamping timing
**Status:** Proposed · **Date:** 2026-06-10

Day-one must-have (makes central TimeSync + datapath capability model load-bearing
from the start) or a later phase (currently phase 4)? **Pending.**

## ADR-0011 — License
**Status:** Proposed · **Date:** 2026-06-10

Prior repos lean BSD-2. Confirm license before first real code lands. **Pending.**

## ADR-0012 — Config surface
**Status:** Proposed · **Date:** 2026-06-10

YAML scenarios are the baseline (see [scenario schema](docs/scenario-schema.md)).
Open: also expose a Go builder API for programmatic scenarios? HCL? **Pending.**

## ADR-0013 — Telemetry transport
**Status:** Proposed · **Date:** 2026-06-10

Stream telemetry over the existing control-plane gRPC connection, or a separate
channel so it never competes with control RPCs? **Pending.**

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
**Status:** Proposed (recommended) · **Date:** 2026-06-10

**Context.** The CLI adapter needs a command/flag framework.
`github.com/bgrewell/stencil` is the house framework (used by sshwiz, testbox,
smart-gateway), has a Claude skill, and provides the build-time version injection
the release flow (ADR-0016) already assumes.
**Decision (proposed).** Build the `loom` CLI on `stencil` and use its dev-CLI for
version/build metadata. Alternative: cobra. **Pending confirmation.**
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
`claude-skills`). `go-iperf`/`go-libiperf` and the WAN-emulation cluster are
decided separately. Full process and disposition in
[docs/eol-plan.md](docs/eol-plan.md).
**Consequences.** Knowledge is preserved independent of the archived repos;
harvest-map `file:line` links keep resolving; git history is retained; the
account gets a clean read-only attic instead of a graveyard.
