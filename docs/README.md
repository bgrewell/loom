# loom documentation

Welcome. loom is a distributed network **traffic generation and measurement**
system — point-and-shoot for a quick test, programmable for a whole fabric. This
manual is layered: every page starts in plain language and goes deeper, so you
can read as far as you need and stop.

## Pick a path

**New here? Read in order:**

1. **[Getting Started](getting-started.md)** — install loom and run your first
   measured flow in a couple of minutes.
2. **[Core Concepts](concepts.md)** — the handful of ideas (flow, datapath,
   scenario, agent…) that make everything else click.
3. **[Guides](guides/README.md)** — task-focused walkthroughs for the things
   people actually do.

**Evaluating loom, or already fluent in this space? Jump to:**

- **[Architecture](architecture.md)** — how it's built and why: the zero-copy
  data plane, the control plane, and the performance model.
- **[Performance](benchmarks.md)** — baseline numbers and how to reproduce them.
- **[Deployment](deployment.md)** — running agents and a controller for real,
  with the security posture spelled out.

## Map of the docs

| Section | What's in it |
|---|---|
| [Getting Started](getting-started.md) | Install, first run, reading the output. |
| [Core Concepts](concepts.md) | The vocabulary and mental model. |
| [Guides](guides/README.md) | How to: single-flow tests, multi-agent scenarios, choosing a datapath, securing the control plane. |
| [The netpath seam](netpath.md) | The injectable connection factory every app dials through. |
| [Application engines](apps.md) | Real protocol engines — voip (RTP/RTCP + MOS), http (TLS/h2), video (ABR player) — and how to place them. |
| [Voice quality scoring](quality.md) | The ITU-T G.107 E-model pipeline behind the voip MOS numbers. |
| [Clocks & one-way delay](clock-sync.md) | Where `owd_ms ± owd_err_ms` comes from and what the method labels mean. |
| [Architecture](architecture.md) | Hexagonal core, data plane, control plane, telemetry, performance. |
| [Deployment](deployment.md) | Running `loomd`/`loomctl`, security, permissions. |
| [Reference: CLI](reference/cli.md) | Every flag and environment variable for `loom`, `loomd`, `loomctl`. |
| [Reference: Scenario schema](scenario-schema.md) | The scenario/timeline file grammar. |
| [Performance](benchmarks.md) | Benchmarks, baselines, how to run them. |
| [Contributing](contributing.md) | Build, test, and extend loom (add a backend). |

## Deeper background

- **[DESIGN.md](https://github.com/bgrewell/loom/blob/main/DESIGN.md)** — the full system design document.
- **[DECISIONS.md](https://github.com/bgrewell/loom/blob/main/DECISIONS.md)** — architecture decision records (ADRs).
- **[Roadmap](roadmap.md)** — what's done and what's next.
- **[blueprints/](blueprints/)** — forward-looking design notes for components
  still being built.
