# CI/CD (draft)

Pipeline and runner topology. Behind
[DESIGN.md §15](../DESIGN.md#15-testing-cicd--performance-regression). Principle:
**the gate that protects `main` runs from the first commit.**

## Runners

- **Cloud (GitHub-hosted):** lint, unit, build matrix, benchmarks, LXD DART.
- **Self-hosted (the physical testbed):** the physical DART + performance tier.
  Runners are labeled by capability so a suite targets the right hardware:
  `loom-testbed`, `nic-100g`, `nic-25g`, `xdp-capable`, `dpdk-capable`. `loomd`
  runs on the testbed hosts; CI triggers DART suites against them.

## Pipeline

On every PR (cloud):

1. **lint** — `gofmt -l` (must be empty), `go vet ./...`, `golangci-lint run`.
2. **unit** — `go test -race -coverprofile ./...`; **coverage gate** fails below
   threshold.
3. **build matrix** — default pure-Go build, plus `-tags afxdp` and `-tags dpdk`
   compile checks (on runners with the libs) so kernel-bypass backends don't
   bit-rot.
4. **bench** — benchmarks on PR + base; `benchstat` flags regressions; hot-path
   **alloc gates** (0 allocs/op) and the **logging-invariant** bench must pass.
5. **integration (LXD)** — `dart -c tests/lxd/*.yaml`.

On merge to `main` / nightly / release (self-hosted testbed):

6. **integration (physical)** — `dart -c tests/physical/*.yaml`.
7. **performance** — capture throughput / latency / loss, compare to committed
   baselines, **fail outside tolerance**, post the delta as a comment, publish to
   the trend store.

## Gates (required checks on `main`)

lint clean · unit + race green · coverage ≥ threshold · benchstat no-regression ·
alloc gates · LXD DART green. The physical perf gate guards release branches.

## Performance comment

A regression surfaces inline, not buried in logs:

> **⚠ perf regression — tcp-100g / socket**
> throughput **8,932 Mbps**, baseline **12,476 ± 5 %** → **−28.4 %** · **FAIL**
> p99 latency 39 µs (baseline 41 ± 20 %) ✓

## Release

- Tags drive releases; version / build metadata is injected at build time via the
  **`stencil` dev-CLI** versioning workflow (consistent with the other repos).
- Artifacts: `loom` (CLI) and `loomd` (agent) binaries; optional container image
  and a `loomd` systemd unit for testbed deployment.
- `goreleaser` for the build/publish matrix.

## Workflow skeleton (drop in at phase 1)

Illustrative — wired up when code lands.

```yaml
# .github/workflows/ci.yml
name: ci
on: [push, pull_request]
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - run: test -z "$(gofmt -l .)"
      - run: go vet ./...
      - uses: golangci/golangci-lint-action@v6

  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - run: go test -race -covermode=atomic -coverprofile=cover.out ./...
      - run: ./scripts/check-coverage.sh core/ 90      # coverage gate

  bench:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - run: ./scripts/benchstat-vs-base.sh            # regression + alloc gates

  integration-lxd:
    runs-on: ubuntu-latest          # or self-hosted with lxd
    steps:
      - uses: actions/checkout@v4
      - run: go install github.com/bgrewell/dart/cmd/dart@latest
      - run: dart -c tests/lxd/two-node-tcp.yaml

  performance:
    runs-on: [self-hosted, loom-testbed, nic-100g]
    if: github.ref == 'refs/heads/main'
    steps:
      - uses: actions/checkout@v4
      - run: dart -c tests/physical/tcp-100g.yaml
      - run: ./scripts/check-baselines.sh tests/baselines/   # fail outside tolerance
```
