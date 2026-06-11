# Blueprint: schedulers (rate / pacing)

**Sources:** blaster `internal/schedulers/{scheduler,token_scheduler,timed_scheduler,soak_scheduler}.go`;
loader `scheduler/bitrate/bitrate.go:47-67`, `core/converters.go:10-62`
**Target:** loom `core/scheduler/`
**Status:** drafted (example blueprint — shows the format)

## Idea

A **Scheduler** decides *when* the next packet is sent within a flow — the
intra-flow pacing loom's [DESIGN §5.2](../../DESIGN.md#52-scheduler--intra-flow-pacing--rate-control)
calls for, and the exact piece `traffic` left as an empty interface. blaster has
three working strategies behind one clean interface; loader contributes a real
bitrate→packets/sec calculation and human rate-string parsing.

## Distilled core

```go
type Scheduler interface {
    Name() string
    // Pace blocks until the next packet should be sent; false ⇒ stop.
    Pace(ctx context.Context) bool
}
```

- **token** — `go.uber.org/ratelimit` token bucket; tokens/sec derived from the
  configured bitrate ÷ payload size.
- **interval** — fixed inter-packet gap: `interval = 1 / packetsPerSecond`, then
  wait until the next send time.
- **soak** — no limit; return immediately (flat-out).
- **bitrate (loader)** — `bitrate ÷ payloadBytes → pps → ratelimit`, plus
  `StringToBitRate` / `BitRateToString` ("100mbit"/"5MB" ↔ bits/sec).

## Why it's good

- One small interface, multiple strategies — the registry-friendly shape loom
  wants ([DESIGN §5](../../DESIGN.md#5-data-plane)).
- blaster's strategies actually run; loader's bitrate math is the only working
  rate-control across all the audited repos.
- Maps 1:1 onto loom's `Scheduler` seam — minimal translation.

## Pitfalls observed (fix on reimplementation)

- **Sub-1.0 interval truncation** — blaster's timed scheduler does
  `time.Duration(interval) * time.Second` on a fractional `interval`, which
  truncates to `0` for any rate > 1 pkt/s, so fast flows silently degrade to
  soak. Compute the gap in **nanoseconds**.
- **Busy-spin** — `for time.Now().Before(next) {}` pins a core per flow. Use
  **sleep-coarse-then-spin** (the goping `main.go:83-91` pattern: sleep while
  >100 µs out, spin the last bit).
- **No accounting** — every scheduler has a `// TODO: accounting`; none count
  bytes. loom builds accounting as a separate concern (don't fold it in here).
- **Unwired cancel** — blaster creates a cancel channel and ignores it. loom's
  `Pace` takes a `context.Context` and honors cancellation.

## loom adaptation

- Implement each strategy as a registry-registered `Scheduler`
  ([DESIGN §5](../../DESIGN.md#5-data-plane)); add `poisson`/`bursty`/`replay`.
- `Pace` must be **allocation-free** on the hot path, use the monotonic clock,
  and run on a pinned pump goroutine ([DESIGN §6](../../DESIGN.md#6-decoupled-logging--telemetry-hard-constraint)).
- Rate strings reuse `go-conversions` rather than re-porting loader's converters.
- Covered by the `SchedulerContract` conformance suite
  ([testing §Tier 2](../testing.md#tier-2--contract--conformance)): honors rate
  within tolerance, stops on ctx cancel, never blocks forever.

## Attribution / license

blaster, loader — © Benjamin Grewell. Relicense the extracted ideas under loom's
license (see [eol-plan §Decide / license](../eol-plan.md)).
