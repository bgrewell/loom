# Testing strategy (draft)

Tests are part of "done" from the first commit. This expands the tiers sketched
in [DESIGN.md §15](../DESIGN.md#15-testing-cicd--performance-regression).

## Testability is architectural

The hexagonal core ([DESIGN §4](../DESIGN.md#4-architecture-overview)) makes
thorough testing cheap:

- `Datapath` / `Scheduler` / `Generator` are interfaces → swap real ones for
  fakes.
- A first-class **`memory` datapath** (in-process loopback; no kernel, NIC, or
  root) lets a full flow — generator → scheduler → pump → datapath → accounting —
  run inside a unit test, deterministically.
- Scenario `seed` makes randomized timing reproducible, so runs are comparable.
- The core never imports an adapter, so nothing needs a CLI, gRPC server, or
  network to be exercised.

## Tier 1 — Unit

- Table-driven; cover happy path, error paths, and boundaries.
- Deterministic: no real network; inject clocks and RNG so there's no wall-clock
  flakiness.
- **Coverage gate**: CI fails if `core/...` coverage drops below threshold (start
  strict, ≈ ≥ 90 %; adapters/`cmd` may be lower). The goal is complete coverage
  of *logic*, not 100 % on trivial glue.
- `go test -race ./...` is mandatory — the data plane is heavily concurrent.

## Tier 2 — Contract / conformance

The registry ([DESIGN §5](../DESIGN.md#5-data-plane)) lets plugins drop in;
contract tests stop them drifting.

- One shared suite per interface — `DatapathContract`, `SchedulerContract`,
  `GeneratorContract` — that every implementation must pass.
- Examples: every `Scheduler` honors its rate within tolerance, stops on context
  cancel, and never blocks forever; every `Datapath` round-trips a frame and
  reports accurate counters; every `Generator` is allocation-free on the hot
  path.
- Adding a backend/protocol = implement the interface + register + pass the
  contract. Nothing else changes.

## Tier 3 — Benchmarks & the hot-path invariant

- Microbenchmarks on the pump inner loop, payloaders, schedulers, accounting.
- **Allocation gates**: assert `0 allocs/op` on the pump hot path
  ([DESIGN §6](../DESIGN.md#6-decoupled-logging--telemetry-hard-constraint)). An
  allocation creeping into the inner loop is a build failure, not a review nit.
- **`benchstat` regression**: CI benchmarks the PR and the base commit and flags
  statistically significant slowdowns.
- **The logging invariant is a benchmark**: a fixed flow is run with logging off
  vs. maxed; achieved rate and pacing distribution must be statistically
  unchanged. This is how §6 is *enforced*, not merely asserted.

## Tier 4 — Integration (DART)

[DART](https://github.com/bgrewell/dart) drives real `loom` / `loomd` binaries via
declarative YAML suites — the same framework used across these repos. Layout:

```
tests/
  smoke.yaml          # build + CLI behavior (version, usage, error handling)
  single-host.yaml    # iperf-esque flow over the memory/lo datapath
  lxd/                # multi-node correctness on LXD containers (cloud CI)
    two-node-tcp.yaml
    mixed-timeline.yaml
  physical/           # real-NIC suites for the testbed (self-hosted runners)
    tcp-100g.yaml
    udp-loss.yaml
  baselines/          # committed perf baselines (Tier 5)
```

- **LXD tier** (cloud CI): spins containers, runs `loomd` on each, drives a
  scenario via the controller, asserts flows ran and reports are sane. Cheap;
  catches wiring/protocol regressions.
- **Physical tier** (testbed): real NICs and rates — correctness *and* the
  performance numbers (Tier 5).
- DART asserts via `match` / `contains` / `exit_code`; numeric perf thresholds
  use a small comparator step invoked from the suite (Tier 5).

**Working within DART's real surface** (per the `dart` skill — DART is
deliberately minimal, and several features its docs advertise aren't
implemented):

- Only the **`execute`** test type exists; assertions are only **`exit_code` /
  `match` / `contains`** — no regex, negation, or numeric comparison. So every
  perf threshold is enforced by a **comparator script** the test runs, asserting
  `exit_code: 0` (Tier 5); DART itself never compares numbers.
- The `execute` step **ignores `timeout:`** — bake `timeout N …` into the
  command.
- **One `local` node** per suite; fan a test across hosts with `node: [a, b, c]`.
- **Node facts** (`{{ fact "<node>" "<name>" }}`) capture a command's output for
  reuse elsewhere in the suite — handy for pulling an interface/IP/host detail.
- Large suites split sections across directories via **`!!load_from(<dir>)`**
  (recursive, lexically-ordered fragments prefixed `00_`, `01_`); the `lxd/` and
  `physical/` trees above adopt that layout as they grow.

## Tier 5 — Performance regression

Why physical hosts exist: catch perf regressions a container can't show.

**Baselines.** For each `(host-pair, scenario, datapath)` we commit a baseline —
the median of N runs — plus a tolerance band:

```yaml
# tests/baselines/tcp-100g.yaml
- key: { hosts: [hostA, hostB], scenario: tcp-100g, datapath: socket }
  throughput_mbps: { median: 12476, tolerance_pct: 5 }
  p99_latency_us:  { median: 41,    tolerance_pct: 20 }
```

**Comparison.** A run extracts metrics from loom's JSON report, compares to the
baseline, and fails outside tolerance:

```
FAIL tcp-100g/socket: throughput 8932 Mbps is 28.4% below baseline 12476 (±5%)
```

**Trend + bisect.** Each run's numbers are also published to a trend store
(a results branch / Grafana / a benchmark-tracking action) keyed by commit, so a
regression can be bisected and improvements stay visible.

**Updating baselines.** Only via a PR that states why (hardware change, an
intentional trade-off). Never silently — a silent baseline bump hides the exact
regressions this tier exists to catch.

## Determinism & flake policy

- Inject clocks and RNG (scenario `seed`); no bare `time.Now()` / `rand` in
  logic under test.
- No network in unit tests; LXD/physical tiers run in their own CI jobs.
- A flaky integration test is **quarantined** (labeled, excluded from the gate)
  and fixed — never left to quietly erode trust in the suite.
