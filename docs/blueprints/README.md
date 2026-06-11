# Blueprints

Ideas harvested from the old repos before they're archived (see
[../eol-plan.md](../eol-plan.md)). Each blueprint captures a capability worth
keeping as a **starting point** — the idea, the key algorithm, the pitfalls — to
be reimplemented cleanly in loom, **not copied**. Code excerpts are illustrative
distillations, not drop-in modules.

## Template

```markdown
# Blueprint: <topic>

**Sources:** <repo> `<file:line>` (+ others)
**Target:** loom `core/<pkg>/`
**Status:** todo | drafted | implemented

## Idea
2–4 sentences: what it is and why it's worth keeping.

## Distilled core
The key interface/algorithm/snippet (illustrative — reimplement clean).

## Why it's good
What it solves; what makes it the best source for this capability.

## Pitfalls observed
The bugs / anti-patterns in the original to avoid on reimplementation.

## loom adaptation
Target package, interface fit, what changes (registry, no-alloc hot path, ctx…).

## Attribution / license
Source repos + author + license note (relicense under loom's license).
```

## Index

| Blueprint | Capability | Primary source(s) | Status |
|---|---|---|---|
| [schedulers](schedulers.md) | rate / pacing strategies | blaster, loader | drafted |
| [payloaders](payloaders.md) | flow data generation | bperf | drafted |
| [accounting](accounting.md) | throughput from byte counters | anapp (concept) + fresh | drafted |
| [latency-probe](latency-probe.md) | latency/jitter/loss sampling | nperfmon | drafted |
| [stats-engine](stats-engine.md) | avg/stddev/CoV/loss/dup math | NetworkPerformanceAnalyzer | drafted |
| [datapath-backends](datapath-backends.md) | socket / afpacket / afxdp / dpdk | packet | drafted |
| [hw-timestamping](hw-timestamping.md) | NIC TX/RX hardware timestamps | basicHWTimestamps, quantify | drafted |
| traffic-engine | flow/endpoint/orchestrator model | traffic | todo |
| control-plane | agent lifecycle proto + handshake | anapp, bperf | todo |
| emulation | app behavior-script primitive | traffic (+ new) | todo |
| dynamic-webserver | random page-size HTTP matrix | traffic | todo |

## Snippets

Verbatim small gems live in [snippets/](snippets/), each with source + license:

| Snippet | What | Source |
|---|---|---|
| checksum | internet (ones-complement) checksum | PacketCraft `helpers.go:51-75` |
| de-bruijn | cyclic payload + offset `Find()` | bperf `payloader/cyclic_payloader.go` |
| hwts-constants | `SO_TIMESTAMPING` / ioctl consts + structs | basicHWTimestamps `timestamp_runner.go:12-66` |
| ntp-math | offset/delay four-timestamp formulas | go-timesync `calc.go:3-9` |
| stats-formulas | jitter/CoV/loss/dup calculations | NetworkPerformanceAnalyzer `netalyzer.go:260-434` |
| sleep-spin | sub-ms inter-packet pacing pattern | goping `main.go:83-91` |
