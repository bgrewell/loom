# Contributing

How to build, test, and extend loom. This is the practical companion to
[Architecture](architecture.md); for the testing philosophy and CI tiers see
[testing.md](testing.md) and [ci-cd.md](ci-cd.md).

## Build & test

```console
go build ./...                       # default (pure-Go) build
go vet ./...
go test -race ./...                  # unit + contract tests, race detector
gofmt -w .                           # format before committing
```

Two things gate the data plane and must stay green:

```console
go test -bench BenchmarkPumpStep ./core/pump/   # hot loop must stay 0 allocs/op
go test -bench BenchmarkTx ./core/datapath/     # datapath throughput (tracked)
```

The AF_XDP backend is behind a build tag and needs a NIC/veth + root, so it's
opt-in and never in the default build or CI:

```console
go build -tags afxdp ./...
sudo LOOM_AFXDP_TEST=1 go test -tags afxdp ./core/datapath/ ./control/
```

### Integration tests (DART)

End-to-end suites run the real binaries via [DART](https://github.com/bgrewell/dart):

```console
dart -c tests/smoke.yaml          # CLI surface
dart -c tests/single-host.yaml    # real flows on one host
dart -c tests/lxd/two-node.yaml   # two LXD containers, controller-driven (needs LXD)
```

The tiers (unit → contract → benchmark → integration → physical-testbed) are
described in [testing.md](testing.md).

## Project layout

See the [repository layout](architecture.md#repository-layout) in the
architecture doc. The short version: all logic lives in the pure `core/` library;
`control/`, `controller/`, and `cmd/` are thin adapters; `proto/`+`api/` hold the
wire contract.

## Adding a backend

Datapaths, generators, schedulers, and payloads are pluggable. To add one (a
datapath here; the others follow the same shape):

1. **Implement the interface** — for a transmit datapath, `TxDatapath`
   (`Name`/`Caps`/`TxReserve`/`TxCommit`/`Close`) in `core/datapath/`. Honor the
   frame borrow contract (don't retain a frame past `TxCommit`).
2. **Register a factory** under a name in the package's `init()` (e.g.
   `Registry.Register("mydp", …)`). Build-tagged backends register the same way
   inside a tagged file. Because the factories self-register into the registries
   that `components.Default()` wraps, the new backend is immediately selectable by
   name — no engine or `switch` changes.
3. **Pass the contract test** — call the shared conformance check for its kind
   from your test (`contract.TxDatapath(t, yourDatapath)`), so it can't drift from
   the interface.
4. **Add a test and, if it's a hot-path type, a benchmark.** New behavior lands
   *with* its tests.

The same pattern applies to `generator.Registry`, `scheduler.Registry`, and
`payload.Registry`, each with a `contract.*` check.

## Changing the wire (proto)

The control plane is defined in [`proto/loom/v1/control.proto`](../proto/loom/v1/control.proto).
Regenerate the Go after editing:

```console
bash scripts/gen-proto.sh        # needs protoc + protoc-gen-go[-grpc]
```

Follow the wire-evolution discipline ([ADR-0021](../DECISIONS.md)): never reuse a
field number, `reserved` removed fields, use enums for closed sets, and bump
`control.APIVersion` on a breaking change.

## Conventions

- Exported symbols get GoDoc comments.
- New decisions are recorded as ADRs in [DECISIONS.md](../DECISIONS.md); design
  changes are reflected in [DESIGN.md](../DESIGN.md); scope lives in the
  [roadmap](roadmap.md).
- Keep changes scoped; the hot-path invariants (0-alloc, non-blocking logging)
  are not negotiable.
