# Blueprint: latency-probe

**Sources:** nperfmon `pkg/pinger/pinger.go`, `pkg/utils/buffer.go` (`CircularBuffer[T]`)
**Target:** loom `core/measure/latency/`
**Status:** drafted

## Idea

An active latency sampler with **typed results and a clean interval model**.
nperfmon's `Pinger` is the only repo that actually measures latency well: each
interval it fires N probes spaced out, collects their RTTs, and emits a batch.

## Distilled core

```go
type PingResult struct {
    Timestamp time.Time
    SeqNum    int
    State     PingState   // ok | timeout | lost | error
    Latency   time.Duration
    Error     error
}

// run(): per interval → fire `Samples` probes spaced by SampleSpacing
// (concurrent) → collect via WaitGroup+chan → emit []PingResult via callback
// → sleep Interval. Distinguishes timeout/lost vs error.
```

`CircularBuffer[T]` is a clean Go-generics fixed-size ring (overwrite on full) for
sliding-window retention — reusable for any metric, including
[accounting](accounting.md).

## Why it's good

- Typed results + explicit `State` (ok/timeout/lost/error) — not just a number.
- The `Start(callback)` + interval-loop shape is the **sink seam**: swap the
  print callback for a real transport/Reporter.
- Same shape as nperfmon's throughput sampler → factor a shared `Collector`
  interface out of both.

## Pitfalls observed

- Needs root (raw ICMP socket); IPv4-only.
- Sequence reuse math `(SeqNum-1) % Samples` is fragile across windows (seq is a
  monotonic global).
- No `context` — uses a bare `running bool`, racy on stop.

## loom adaptation

- Generalize into a `Collector` (`Start(ctx, emit) / Stop()`) with **context**,
  a name, and typed samples — the interface none of the old repos had.
- Support ICMP, UDP-probe, and **in-band** latency (timestamps in the data flow's
  patterned payload), feeding the [stats-engine](stats-engine.md).
- Reuse `CircularBuffer` for windows.

## Attribution / license

nperfmon — © Benjamin Grewell. Relicense under loom's license.
