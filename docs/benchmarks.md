# Benchmarks

How to run loom's microbenchmarks and the baseline numbers we track. These are
**single-core, single-flow** Go benchmarks for tracking relative performance and
catching regressions (with `benchstat`); they are not line-rate claims. Real
NIC numbers come from the physical testbed (Tier 5, [ADR-0016](../DECISIONS.md)).

## Engine hot loop (no datapath)

```
go test -bench BenchmarkPumpStep -benchmem ./core/pump/
```

The pump's generate → pace → send → account loop over the discard sink. Gated to
**0 allocs/op** (the [DESIGN §6](../DESIGN.md) decoupled-logging invariant); the
"with logging" variant must stay 0-alloc too.

## Datapath throughput

```
go test -bench BenchmarkTx -benchmem ./core/datapath/
```

`b.SetBytes` makes each report MB/s. The TX path is driven one frame per
reserve/commit — matching how the pump drives it today.

AF_XDP needs the tag, root, and an opt-in (it sets up a throwaway veth pair):

```
sudo LOOM_AFXDP_TEST=1 go test -tags afxdp -bench AFXDP -benchtime 2s ./core/datapath/
```

## Baseline (Intel Xeon Platinum 8280 @ 2.70GHz, Linux 6.8, 1400-byte frames)

| Benchmark | Rate | Allocs | What it measures |
|---|---|---|---|
| `BenchmarkPumpStep` | 16.99 M pps (58.9 ns) | 0 | engine ceiling, generation only |
| `BenchmarkPumpStepWithLogging` | 12.36 M pps (80.9 ns) | 0 | engine with hot-path logging on |
| `BenchmarkTxDiscard` | ~743 Gbps (15.1 ns) | 0 | frame machinery, no I/O |
| `BenchmarkTxMemory` | ~155 Gbps (72.2 ns) | 0 | in-process zero-copy loopback (TX+RX) |
| `BenchmarkTxAFXDPVeth` | ~19.0 Gbps (588 ns) | 0 | AF_XDP zero-copy over **veth** (batch 64) |
| `BenchmarkTxUDPSocket` | ~2.16 Gbps (5174 ns) | 3 | kernel-stack UDP send over loopback |

End-to-end real binary, `loom run -d discard` soak (single flow): **172 Gbps**
at 1400 B, **503 Gbps** at 9000 B.

### Reading these
- **discard/memory** measure the software, not a wire — the engine is not the
  bottleneck (~170 Gbps/core generation, ~500 Gbps jumbo).
- **veth is a kernel software pipe, not a NIC.** The AF_XDP number is bounded by
  the kernel veth path + one `poll()` per batch, not by loom or real hardware; a
  real NIC with AF_XDP zero-copy reaches NIC line rate.
- **UDP socket** is the kernel-stack ceiling per flow (~2 Gbps single-stream) —
  ~9× below AF_XDP/veth, which is why kernel-bypass exists.
- The pump commits **one frame per syscall** today; batched pacing (a follow-up)
  is what lets a real datapath approach the engine's pps over a NIC.
- Aggregate scales ~linearly with independent flows across cores (this box has
  112 threads).
