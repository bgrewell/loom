# Old-repo extraction & EOL plan (draft)

How we harvest the useful ideas out of ~30 prior traffic/measurement repos and
then retire them — without losing anything worth keeping.

## Principles

1. **Extract knowledge before archiving.** loom must not depend on spelunking
   archived repos; the gems become first-class **blueprints** in loom
   ([docs/blueprints/](blueprints/)).
2. **Archive, don't delete.** `gh repo archive` marks a repo read-only and signals
   EOL but keeps it browsable and cloneable — so the harvest map's `file:line`
   pointers keep resolving. Deletion is reserved for local-only scratch with
   nothing worth keeping.
3. **Blueprints are ideas, not imports.** Capture the design, the key algorithm,
   and the pitfalls; reimplement cleanly in loom (per ADR-0001/0006). Explicitly
   *"probably not used as-is."*
4. **No silent loss.** A repo isn't archived until its value is captured (a
   blueprint or snippet exists) or it's explicitly recorded as nothing-to-keep.
5. **Every retired repo leaves a trail** — an epitaph plus a pointer to loom (and
   to its blueprint, if any).

## What "extraction" produces

Three artifact types, all living in loom so they outlive the source repos:

- **Harvest map** — the index of *component → source repo → action*. Already in
  [DESIGN.md Appendix A](../DESIGN.md#appendix-a--harvest-map).
- **Blueprints** (`docs/blueprints/<topic>.md`) — one per harvested capability:
  the idea, the distilled core (snippet/pseudocode), why it's good, the pitfalls
  to avoid, how loom adapts it, and attribution/license. Template and index in
  [docs/blueprints/README.md](blueprints/README.md).
- **Reference snippets** (`docs/blueprints/snippets/`) — small, proven code worth
  keeping verbatim (internet checksum, De Bruijn generator, HW-timestamp
  constants, NTP offset/delay math, stats formulas), each with source + license.

## Repo disposition

> Buckets, not a full per-repo essay. Completion levels and what's-worth-it come
> from the audit; this is the *decision*.

### Keep — active tooling/deps (not EOL)
`dart`, `stencil`, `go-conversions`, `claude-skills`, and `loom` itself. loom
depends on these; they're infrastructure, not legacy.

### Harvest → blueprint → archive
| Repo | Extract into |
|---|---|
| `traffic` | `traffic-engine` (flow/endpoint/orchestrator interfaces), `emulation`, `dynamic-webserver` |
| `blaster` | `schedulers` |
| `bperf` | `payloaders`, `control-plane`; snippet `de-bruijn` |
| `loader` | `schedulers` (bitrate strategy + rate converters) |
| `nperfmon` | `latency-probe` |
| `packet` | `datapath-backends` |
| `basicHWTimestamps` | `hw-timestamping`; snippet `hwts-constants` |
| `NetworkPerformanceAnalyzer` | `stats-engine`, `reflector`; snippet `stats-formulas` |
| `anapp` | `control-plane`, `accounting`, conn TCP/UDP unification |
| `quantify` | `hw-timestamping` (RX reference) |
| `perspective` | agent poll→fan-in pipeline (reference) |
| `PacketCraft` | snippet `checksum` |
| `goperf` | native-generator interface shape (reference) |
| `conductor-plugin-udpsender` | plugin command-registry pattern (reference, optional) |

### Archive — nothing to extract (stub / superseded)
`gperf`, `netben`, `go-iperfmod`, `locast`, `npass`, `ratemon`,
`NetworkApplicationProfiler`, `NetMeasure`, `NetworkLatencyAnalyzer`,
`corr-jitter-test`, `ethercomms`, `linkperf`, `goping` *(tiny; only the
sleep-then-spin pacing idea, captured as a snippet note)*.

### Delete — local-only scratch (no GitHub repo)
`loadly` (unpushed), `sshsim` (no git). Confirm nothing to keep, then remove the
local directory.

### Decide separately (out of this pass)
- `go-iperf`, `go-libiperf` — iperf-bound; standalone wrappers, not part of loom's
  native engine. Keep as standalone or archive — **your call.**
- **WAN-emulation cluster** (`wanem*`, `wemo*`, `smart-wan`, `diversion`,
  `shaping-controller`, `go-netqospolicy`) — a different category (traffic
  *impairment*, not generation); its own consolidation later.

## Extraction workflow (per repo)

1. Confirm its row in the disposition table / harvest map.
2. Write the blueprint(s) and/or snippet(s) it contributes — or record
   "nothing to keep."
3. Add an epitaph row to the ATTIC table (below) with the blueprint link.

## Archiving procedure (per repo, after extraction)

Archiving makes a repo read-only, so order matters:

1. Add an **EOL notice** to the top of the repo's README on its default branch:
   > ⚠️ **Archived / superseded by [loom](https://github.com/bgrewell/loom).**
   > Useful ideas were extracted to loom (`docs/blueprints/…`). Kept read-only for
   > reference.
2. `gh repo archive bgrewell/<repo>`.
3. Confirm the epitaph row in loom's ATTIC.

Local-only scratch: just delete the directory after confirming nothing to keep.

## Sequencing

1. **Decide** — finalize this disposition table.
2. **Extract phase-1 gems first** — the blueprints loom's MVP needs (`schedulers`,
   `payloaders`, `accounting`, `latency-probe`, `stats-engine`,
   `datapath-backends`, `hw-timestamping`). Land them in loom.
3. **Snapshot the rest** — lighter blueprints/epitaphs for the reference-only repos.
4. **Notice + archive in batches** — stubs first (lowest risk), then the
   harvested-and-captured repos.
5. **Verify** — every harvest-map link still resolves; nothing referenced is lost.

## Tracking

A loom issue/project — *"Repo consolidation & EOL"* — holds the per-repo
checklist: **extracted? · notice added? · archived? · epitaph recorded?**

## ATTIC (epitaphs)

Filled in as repos are retired. One row per repo.

| Repo | Was | Extracted to | Disposition | Date |
|---|---|---|---|---|
| _…_ | | | | |
