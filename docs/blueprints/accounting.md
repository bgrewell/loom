# Blueprint: accounting (throughput from byte counters)

**Sources:** anapp `shared/accounting/accountant.go` (**concept only** — reimplement)
**Target:** loom `core/accounting/`
**Status:** drafted · **action: BUILD FRESH**

## Idea

Turn raw byte/packet counters into **rate over time** — the "count of bits per
unit of time" that is literally the point of a traffic tool, and that **every
generator in the audit left as a `// TODO`** (traffic, blaster, bperf, loader all
lack it). anapp is the only attempt: a background goroutine samples a byte counter
once a second and keeps a sliding window of per-second rates.

## Distilled core

```
hot path:        atomic.AddUint64(&bytes, n)        // lock-free, per worker
sampler (1 Hz):  delta = bytes - last; last = bytes
                 push(delta) into a ring of per-second samples
expose:          current = ring.last
                 avg/peak = over window
```

The counters incremented on the hot path are the **same atomics** the §6
telemetry pipeline reads out-of-band — accounting and decoupled metrics are one
mechanism, not two.

## Why it's good

- It's the missing heart of the whole product; nothing else implements it.
- The shape (counter → fixed-interval sampler → windowed rate) is simple and
  correct.

## Pitfalls observed (anapp's impl — do NOT copy)

- Busy-wait `time.Sleep(10µs)` loop instead of a ticker.
- Slice-doubling typo `2+len(...)`; `idx` underflow when `samples > idx`.
- No mutex/atomics on the counters — racy.

Build clean: **atomic counters** (sharded per worker), a **`time.Ticker`**, and a
**ring buffer** (reuse the generic `CircularBuffer` from the
[latency-probe](latency-probe.md) source).

## loom adaptation

- Per-flow and aggregate accounting; `Add` is a single atomic, **zero alloc**.
- Sampler runs off-path; current/avg/peak exposed to the
  [Reporter](https://github.com/bgrewell/loom/blob/main/DESIGN.md#7-measurement-plane) and the telemetry stream.
- Pair with the patterned [payloader](payloaders.md) so loss/dup are accounted
  alongside throughput.

## Attribution / license

anapp (concept) — © Benjamin Grewell. Implementation is new.
