# Scenario & Timeline schema (draft)

The scenario file is how a user declares **what traffic runs, between which
points, when, and for how long** — including overlap and randomized timing. This
is the detailed grammar behind [DESIGN.md §9](https://github.com/bgrewell/loom/blob/main/DESIGN.md#9-orchestration-scenarios--timeline).

> Draft for discussion. Field names, defaults, and the value grammar are all up
> for debate — this is a riff surface, not a frozen spec.

## Top-level shape

```yaml
scenario:    string            # name (required)
description: string            # optional
seed:        int               # optional RNG seed → reproducible randomized runs
defaults:                      # optional, applied to every flow unless overridden
  datapath:  socket
  scheduler: { kind: soak }
endpoints:   [ Endpoint, ... ] # the named points traffic runs between
timeline:    [ Event, ... ]    # what runs when (the core of the file)
report:      Report            # optional reporting/sinks config
```

A scenario with a `seed` replays identically (same random intervals, sizes,
selections) — critical for comparing runs. Omit it for fresh randomness.

## Value grammar (reusable everywhere)

Sizes, durations, intervals, and think-times all share one grammar, so the same
forms work for `interval`, `object_size`, `after`, etc.

| Form | Example | Meaning |
|---|---|---|
| scalar | `100ms`, `1.5M`, `123MB`, `1400` | a fixed value |
| range (uniform) | `10ms..100ms`, `100KB..3MB` | uniform random in `[lo, hi]` |
| distribution | `{ dist: normal, mean: 50ms, stddev: 10ms }` | sampled per use |

Distributions: `uniform {min,max}`, `normal {mean,stddev}`, `exponential {mean}`,
`constant {value}`, `lognormal {mean,stddev}`. Units: time `ns/us/ms/s/m/h`. Sizes
and rates follow the SI/IEC split: SI decimal prefixes `K/M/G/T` are powers of 1000
(`100MB` = 100 000 000 bytes, `100Mbps` = 100 000 000 bit/s), while IEC binary
prefixes `Ki/Mi/Gi/Ti` are powers of 1024 (`100MiB` = 104 857 600 bytes,
`100Mibps` = 104 857 600 bit/s) — so `100MB` and `100MiB` are **not** the same. A
trailing `B`/`bps`/`bit` is optional. Bare numbers are bytes (size) or nanoseconds
(time) — **TBD, see open questions**.

## Endpoint

```yaml
endpoints:
  - name: client          # required, unique
    tags: [vm, 10g, linux] # optional, used by selectors
    address: 10.0.0.11     # optional hint; usually resolved via the agent/control plane
```

Endpoints are logical names bound to agents at run time by the controller. A flow
picks its `from`/`to` from these via selectors (below).

## Selectors (`from` / `to`)

| Form | Example | Meaning |
|---|---|---|
| name | `from: client` | exactly that endpoint |
| list + mode | `from: { oneOf: [a, b, c] }` | pick one (also `allOf`, `any`) |
| tag expr | `to: tags(all(10g, linux))` | endpoints matching the tag expression |
| meta | `from: any` / `from: all` | random one / every endpoint |

Tag expression operators: `all(...)`, `any(...)`, `not(...)`, nestable —
`tags(all(10g, not(win)))`. The orchestrator enforces **client ≠ server** so a
node never selects itself for both sides.

## Event (a timeline entry)

```yaml
- name:      web                      # required, unique within the scenario
  flow:      Flow                     # what traffic (see below)
  from:      Selector                 # source endpoint(s)
  to:        Selector                 # destination endpoint(s)
  datapath:  afxdp                    # optional, overrides defaults
  scheduler: { kind: token, rate: 200Mbps }  # optional intra-flow pacing override
  start:     0s | +45s | { at: "12:00:00" }  # when to (first) fire
  repeat:    Repeat                   # optional; omit = fire once
  stop:      Stop                     # when to end each instance
  count:     1                        # optional; parallel instances per fire
```

### `start`
- `0s` / `30s` — relative to scenario start.
- `+45s` — explicit relative offset (same as `45s`; the `+` is sugar for clarity).
- `{ at: "<wallclock>" }` — absolute time (requires cross-node TimeSync; see open
  questions).

### `repeat` (recurring instances → this is where "random timing" lives)
```yaml
repeat:
  interval: 10ms..100ms     # value grammar → random gap between fires
  jitter:   uniform         # how `interval` ranges/dists are sampled (default uniform)
  count:    100             # optional cap on number of fires
  until:    +60s            # optional time/condition to stop firing
```
Omitting `repeat` ⇒ the event fires once at `start`.

### `stop` (bounds each instance)
| Form | Example | Meaning |
|---|---|---|
| keyword | `end-of-test` | run until the scenario ends |
| duration | `{ after: 1m }` | run for a wall-clock duration |
| volume | `{ volume: 123MB }` | run until N bytes transferred |
| count | `{ count: 10000 }` | run until N packets/requests |

**Overlap is the default:** events are independent, so any number run
concurrently — two events with overlapping `[start, stop]` windows simply
coexist. There is no implicit serialization to opt out of.

## Flow

`flow.kind` selects a raw protocol or an emulation; remaining keys are its params.

### Raw protocols
```yaml
flow: { kind: tcp,  rate: 100Mbps, packet_size: 1400, direction: both }     # toClient|toServer|both
flow: { kind: udp,  rate: 50Mbps,  packet_size: 1200, direction: toClient }
flow: { kind: icmp, interval: 1s }
flow: { kind: quic, rate: 200Mbps }
```

### Emulations (see [DESIGN.md §10](https://github.com/bgrewell/loom/blob/main/DESIGN.md#10-traffic-emulation))
```yaml
flow: { kind: https-browse,     object_size: 100KB..3MB, think: 200ms..2s, keepalive: true }
flow: { kind: voip-call,        codec: g711, duration: 30s }     # CBR bidirectional
flow: { kind: ssh-session,      interkey: 80ms..300ms, bulk: 0..2MB }
flow: { kind: prometheus-sender, scrape: 15s, series: 5000 }
flow: { kind: ftp-transfer }                                     # bound by stop.volume
```

## Report (optional)
```yaml
report:
  interval: 1s                 # streaming sample cadence
  sinks:
    - { kind: stdout, format: human }   # human | json
    - { kind: file, path: run.json, format: json }
    - { kind: prometheus, listen: ":9100" }
    - { kind: socket, addr: "tcp://collector:9000" }
```

## Worked example

The three flows from the design discussion, plus a few features (tag selection,
multi-instance, datapath override, reproducible seed):

```yaml
scenario: branch-office-mix
description: Mixed app traffic from branch clients to the edge.
seed: 1337
defaults:
  datapath: socket
  scheduler: { kind: soak }

endpoints:
  - { name: client-a, tags: [vm, 10g, linux] }
  - { name: client-b, tags: [vm, 10g, linux] }
  - { name: edge,     tags: [server, 40g]   }

timeline:
  # HTTP every 10–100 ms (random), object 100K–3M, whole run, overlapping,
  # from any 10g linux client to the edge.
  - name: web
    flow:   { kind: https-browse, object_size: 100KB..3MB, think: 200ms..2s }
    from:   tags(all(10g, linux))
    to:     edge
    start:  0s
    repeat: { interval: 10ms..100ms, jitter: uniform }
    stop:   end-of-test

  # SSH session starting 45 s in, runs for 1 minute, single client.
  - name: admin-ssh
    flow:  { kind: ssh-session }
    from:  client-a
    to:    edge
    start: +45s
    stop:  { after: 1m }

  # FTP transfer starting 37 s in, bounded by volume (123 MB), over afxdp.
  - name: backup
    flow:     { kind: ftp-transfer }
    from:     client-b
    to:       edge
    datapath: afxdp
    start:    +37s
    stop:     { volume: 123MB }

  # 4 concurrent VoIP calls at 20 s in, 30 s each, any client → edge.
  - name: voip
    flow:  { kind: voip-call, codec: g711 }
    from:  { any: [client-a, client-b] }
    to:    edge
    start: +20s
    count: 4
    stop:  { after: 30s }

report:
  interval: 1s
  sinks:
    - { kind: stdout, format: human }
    - { kind: file, path: branch-office-mix.json, format: json }
```

## Semantics & validation notes

- **Capability validation:** at arm time the controller checks each flow's
  `datapath`/`kind` against the target agent's advertised capabilities and fails
  fast (e.g. `afxdp` requested on a NIC without XDP support).
- **Determinism:** with `seed` set, all randomized intervals/sizes/selections are
  reproducible. The RNG is scenario-scoped and split per event so adding an event
  doesn't perturb others' streams.
- **Time base:** relative `start`/`stop` are measured from a single scenario
  epoch broadcast to all agents; absolute `{ at: }` needs cross-node TimeSync.
- **Overlap:** there is no implicit serialization — concurrency is the default and
  the point.

## Open questions (riff here)

1. **Bare-number units** — default size→bytes, time→ns? Or require explicit units
   always (safer, more verbose)?
2. **Dependencies/sequencing** — only time-based triggers, or also "start `B`
   when `A` finishes"? (A DAG is more powerful but heavier than a timeline.)
3. **Ramps** — first-class `ramp` (rate/clients increasing over a window), or
   express via `repeat` + a changing `rate` distribution?
4. **Absolute wall-clock scheduling** across nodes — worth supporting day one, or
   relative-only until TimeSync is solid?
5. **Per-instance vs per-event accounting** — when `repeat`/`count` spawn many
   instances, do we report them individually, rolled up, or both?
6. **Selection stability** — for a repeating event with `oneOf`, re-select an
   endpoint every fire, or pin the selection for the event's lifetime?
