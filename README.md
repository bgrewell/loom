# loom

> **Status: planning / RFC.** No code yet — this repo currently holds a design
> document we're iterating on. The name `loom` is **provisional** (see
> [Open Decisions](DESIGN.md#13-open-decisions)).

**loom** is a modern, distributed network **traffic generation and measurement**
system for Linux. It weaves many independent traffic flows — raw protocols and
realistic application emulations — across many points, on a schedule you define,
and measures what happens, in real time.

It's the consolidation of a decade of scattered, half-finished traffic and
network-measurement projects into one clean core library with thin CLI / API /
web / control-plane adapters around it.

## What it's for

- **iperf-esque quick tests** — one command, point-to-point throughput/latency.
- **Complex multi-point scenarios** — N agents, overlapping flows, randomized
  timing, tag-based endpoint selection, driven by a declarative scenario file.
- **Application emulation** — HTTPS browsing, VoIP calls, SSH sessions,
  Prometheus remote-write, FTP transfers — not just raw packet blasting.
- **Pluggable everywhere** — schedulers, packet pumps, and datapaths
  (kernel sockets → AF_PACKET → AF_XDP → DPDK) are all swappable.
- **Measurement-first** — streaming and end-of-run reporting of throughput,
  latency, jitter, loss, one-way delay (optional NIC hardware timestamping).

## The design

The full architecture is in **[DESIGN.md](DESIGN.md)**. This repo exists to be
discussed and riffed on — open issues, comment on the RFC PR, or just edit the
doc.

## Background

This grew out of an audit of ~35 prior projects (tgams, traffic, blaster, bperf,
nperfmon, packet, and many more). [DESIGN.md, Appendix A](DESIGN.md#appendix-a--harvest-map)
records exactly which working pieces we plan to lift from where.
