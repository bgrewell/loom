# Blueprint: payloaders (flow data generation)

**Sources:** bperf `payloader/{payloader,patterned_payloader,random_payloader,cyclic_payloader}.go`
**Target:** loom `core/generator/payload/`
**Status:** drafted

## Idea

A **Payloader** produces the bytes a flow sends, behind a plain `io.Reader`. bperf
has the best-implemented data generation of all the audited repos — three
self-contained, ring-buffered strategies that drop straight into any sender.

## Distilled core

```go
type Payloader interface { Read(p []byte) (int, error) } // io.Reader
```

- **patterned** — sequence-numbered 30-byte groups (big-endian counter + `A`–`Z`).
  The sequence numbers let the *receiver* detect loss, reorder, and duplication
  — it feeds the measurement side, not just fills bytes.
- **random** — a pre-filled random buffer, ring-read (cheap, no per-read RNG).
- **cyclic** — a **De Bruijn sequence** (DFS over a k-ary alphabet) with a
  `Find(substr)` that returns the offset — useful for payload-offset diagnostics.

## Why it's good

- Clean `io.Reader` contract → composes with any transport/datapath.
- Ring-buffered → no allocation per read once primed.
- The patterned generator is the bridge to loss/reorder/dup measurement
  (pairs with the [stats-engine](stats-engine.md)).

## Pitfalls observed

- The payloaders themselves are sound; what's broken in bperf is *around* them —
  the schedulers and data plane are stubs ([see schedulers blueprint](schedulers.md)).
  Take the payloaders, not bperf's transmit path.
- Don't reuse `packet`'s `RandomPayloader` instead — it `panic`s on
  `Encode/Decode/Size` and returns overlapping slices of a never-filled buffer.

## loom adaptation

- Implement as registry-registered payload sources used by `Generator`s
  ([DESIGN §5.3](https://github.com/bgrewell/loom/blob/main/DESIGN.md#53-generator--what-the-traffic-is)).
- Preallocate + ring-read so `NextPayload` is **allocation-free** on the hot path
  ([DESIGN §6](https://github.com/bgrewell/loom/blob/main/DESIGN.md#6-decoupled-logging--telemetry-hard-constraint)).
- Keep the patterned generator's seq framing as the standard for in-band
  loss/reorder/dup detection.
- De Bruijn payload is an opt-in diagnostic mode.

## Attribution / license

bperf — © Benjamin Grewell. Relicense under loom's license.
