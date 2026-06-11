# Blueprint: stats-engine

**Sources:** NetworkPerformanceAnalyzer `netalyzer.go:260-434` (stats);
`udp{sender,receiver,reflector}.go`, `structs.go:194-234` (`ProfilePacket`, 4-timestamp scheme)
**Target:** loom `core/measure/stats/`
**Status:** drafted

## Idea

The **measurement math** — the richest in the audit. Computes per-direction
min/max/avg latency, standard deviation, **coefficient of variation**, loss %,
**duplicate and reorder detection**, and inter-packet send/receive spacing. Plus a
**4-timestamp reflector** (T1 send, T2/T3 reflect, T4 receive) for RTT/jitter.

## Distilled core

```
streaming accumulation: n, sum, sumsq → mean, stddev, CoV = stddev/mean
loss   = expected - received
dup    = seq seen more than once        (requires seq-numbered payload)
reorder= seq arriving out of order
ProfilePacket { seq, t1, t2, t3, t4 }   // carried in the flow's payload
```

## Why it's good

- The genuinely valuable measurement IP — the "measurement" half of the product.
- The 4-timestamp `ProfilePacket` is a clean RTT/OWD-ready framing that pairs with
  the reflector role and the patterned [payloader](payloaders.md).

## Pitfalls observed

- Busy-wait spin loops for send timing (pins a core).
- Package-global mutable state; `os.Exit` on error (`CheckError`).
- Software `time.Now()` timestamps only (hardware path lives in
  [hw-timestamping](hw-timestamping.md)).

## loom adaptation

- Reimplement as **pure functions over sample streams** — no globals, no
  `os.Exit`, returns errors.
- Drive sequence numbers from the patterned payloader; reflector becomes a
  `Generator`/role on the agent.
- Output flows into the [Reporter](../../DESIGN.md#7-measurement-plane) (streaming
  + end-of-run). Feed both software and (optional) hardware timestamps in.

## Attribution / license

NetworkPerformanceAnalyzer — © Benjamin Grewell. Algorithms reimplemented clean.
