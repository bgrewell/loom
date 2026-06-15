# Application emulations

Raw `udp`/`tcp` flows blast bytes; **emulations** reproduce the *traffic shape* of
real applications — a VoIP call's steady tiny packets, a web browse's bursty
object fetches with reading pauses, Prometheus's periodic scrapes. They run on a
shared **behavior-script engine**, so each app is a small script over the same
machinery, not a bespoke client.

## How they work

An emulation compiles to a **behavior script** — a sequence of steps, each
"*send an object of this size, then wait this long*", where size and wait are
**distributions** (a fixed value or a `lo..hi` range). The engine repeats the
script over the flow's datapath until the stop condition, accounting every byte.
Because the gaps and sizes are seeded, a run reproduces exactly.

An emulation is the **sender's** behavior; an ordinary receiver absorbs and
measures it. (Emulations that depend on the *server's* response size — a real
HTTPS server, an FTP data channel — build on this same engine once the responder
role lands; today emulations model the client-side shape.)

## Using one

Set the event's `flow.kind` to an emulation name and pass its parameters in the
flow block:

```yaml
scenario: branch-traffic
seed: 1
endpoints:
  - name: phone
    address: 10.0.0.11
  - name: pbx
    address: 10.0.0.12

timeline:
  - name: a-call
    flow:
      kind: voip-call          # the emulation
      codec: g711              # its parameters
    from: phone
    to: pbx
    start: 0s
    stop:
      after: 60s
```

Run it with `loomctl` as usual ([multi-agent guide](multi-agent-scenario.md)).
Any agent can run any emulation — they're built in.

## The launch emulations

| `kind` | Shape | Key params (defaults) |
|---|---|---|
| `voip-call` | constant-bit-rate media | `codec` (`g711`=160 B/20 ms, `g729`), `frame_size`, `interval` |
| `https-browse` | a keep-alive session of object fetches with reading pauses | `objects` (`10`), `object_size` (`8KB..512KB`), `think` (`200ms..2s`) |
| `prometheus-sender` | periodic remote-write batches | `scrape` (`15s`), `batch_size` (`64KB`) |
| `ssh-session` | interactive keystrokes, optional bulk (scp) | `keys` (`100`), `key_size` (`1..64`), `interkey` (`80ms..300ms`), `bulk` (`0`) |

Sizes and times accept the standard [value grammar](../scenario-schema.md): a
scalar (`160`, `64KB`, `20ms`) is a fixed value; a `lo..hi` range
(`8KB..512KB`, `200ms..2s`) is sampled uniformly per step. Combine with any
[stop condition](../reference/cli.md) (`stop.after`/`count`/`volume`) and a
`seed` for reproducibility.

## Examples

```yaml
# a bursty web user reading ~20 pages
- name: browse
  flow: { kind: https-browse, objects: 20, object_size: 16KB..1MB, think: 500ms..4s }
  from: client
  to: web
  start: 0s
  stop: { after: 5m }

# an SSH session with a 50 MB scp at the end
- name: shell
  flow: { kind: ssh-session, keys: 300, interkey: 60ms..250ms, bulk: 50MB }
  from: laptop
  to: jumphost
  start: +2s
  stop: { after: 2m }
```

## Notes

- Emulations run over whatever datapath the event selects (`udp` by default);
  measurement (throughput now; jitter/loss for CBR as the stats path lands) comes
  from the same accounting/telemetry as raw flows.
- A raw `kind` (`udp`, `tcp`, `stream`) is *not* an emulation — it runs the plain
  generator path.
- Adding a new emulation is a small script compiler plus a registry entry — see
  [Contributing](../contributing.md).
