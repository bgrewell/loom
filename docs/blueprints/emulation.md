# Blueprint: emulation (application behavior-script primitive)

**Sources:** traffic `network/endpoint/httpEndpoint.go` (HTTP client),
`network/webserver/` (see [dynamic-webserver](dynamic-webserver.md)); the
per-protocol session idea. VoIP/SSH/etc. are **new**.
**Target:** loom `core/generator/emul/`
**Status:** drafted · **action: mostly FRESH, reference traffic's HTTP**

## Idea

Emulate the *traffic shape* of real applications (https-browse, voip-call,
ssh-session, prometheus-sender, ftp-transfer) by compiling each to a shared
**behavior-script primitive** — a sequence of
`(direction, size-dist, think-time-dist, transport)` steps — so they share one
engine instead of each being bespoke
([DESIGN §10](../../DESIGN.md#10-traffic-emulation)).

## Distilled core

```go
type Step struct {
    Dir       Direction      // toServer | toClient | both
    Size      Dist           // bytes
    Think     Dist           // gap before/after
    Transport Transport      // tcp | tls | udp | quic
}
type BehaviorScript []Step   // an Emulation compiles to one of these
```

| Emulation | Script shape |
|---|---|
| https-browse | TLS + N GETs of `object_size` dist with `think` gaps, keep-alive |
| voip-call | bidirectional CBR UDP (G.711: 160 B / 20 ms), duration-bound |
| ssh-session | TCP interactive bursts w/ inter-key timing + optional bulk (scp) |
| prometheus-sender | periodic remote-write POSTs at `scrape` interval |
| ftp-transfer | control + data channel, volume-bound bulk |

## Why it's good

- One primitive → all emulations share machinery; a new app = a new script, not a
  new transport.
- The behavior-script maps cleanly onto loom's `Generator`
  ([DESIGN §5.3](../../DESIGN.md#53-generator--what-the-traffic-is)) and the
  [scenario `flow.kind`](../scenario-schema.md) params.

## Pitfalls observed

- traffic's UDP/VoIP/QUIC endpoints are **no-op stubs** — don't reuse them.
- Its HTTP endpoint is **GET-only client**; the reusable HTTP piece is the
  server (see [dynamic-webserver](dynamic-webserver.md)).

## loom adaptation

- `Emulation` is a `Generator` built from a `BehaviorScript`; the engine runs the
  script over the chosen `Datapath`.
- https-browse drives the [dynamic-webserver](dynamic-webserver.md); voip uses a
  CBR [scheduler](schedulers.md); measurement (jitter/loss) comes free from the
  [stats-engine](stats-engine.md) + patterned [payloader](payloaders.md).

## Attribution / license

traffic (HTTP/webserver reference) — © Benjamin Grewell. Emulations are new.
