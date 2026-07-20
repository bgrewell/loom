# Application engines

Emulations reproduce an application's traffic *shape*
([guide](guides/emulations.md)). **Apps** are the next tier up: real protocol
engines — actual RTP/RTCP with live MOS scoring, actual HTTP/1.1 + TLS — that
loom generates, answers, and scores itself. A capture of an app flow decodes
in Wireshark because it *is* the protocol, not a silhouette of one.

Three engines ship today: **`voip`** (RTP/RTCP media with ITU-T G.107
scoring), **`http`** (HTTP/1.1 + TLS 1.3, optional h2, client and origin),
and **`video`** (an ABR player over the `http` origin). This page covers the
framework they share and each one as a worked example.

## The framework: `core/app`

An app is a **Client** (caller, fetcher, player) or a **Server** (answerer,
origin) with a deliberately small contract:

```go
type Client interface {
    Name() string
    Run(ctx context.Context) error
    Counters() *accounting.Counters
}

type Server interface {
    Client's methods…
    Addr() netip.AddrPort // bound address, valid at build time
}
```

Both are `flow.Runner`-compatible, which is the whole trick: the agent's
existing flow lifecycle — Configure/Start/Stop, panic containment, telemetry
boundaries — applies to an app exactly as it does to a raw sending flow.
`Server.Addr()` is valid **at build time** (servers bind eagerly in their
factory), so the agent can advertise the bound port at Configure, before Run —
the same bound-port readback pattern as the receiver datapaths.

### Options and params

One struct configures an instance at build time:

```go
type Options struct {
    Params  map[string]string  // the app's knobs; each app documents its keys
    Seed    int64              // deterministic randomness
    MTU     int                // datagram bound for packet-oriented apps
    Network netpath.Network    // the connection factory — the only way out
    Target  string             // client side: the server's host:port
    OWD     owd.OffsetProvider // nil ⇒ RTT/2 fallback, labeled as such
}
```

Two fields carry the seams that make apps composable. `Network` is the
[netpath seam](netpath.md): an app dials and listens **only** through it,
never `net.Dial`/`net.Listen`, so the same engine runs over the kernel, a
tunnel lane, or the in-memory test fabric. `OWD` is the
[clock seam](clock-sync.md): nil is legal and honest — the app falls back to
RTT/2 and *labels* it.

`Params` are stringly on the wire (they ride `FlowSpec.params` verbatim) but
not in code: `app.NewParams` gives typed access (`GetInt`, `GetBool`,
`GetDuration`, …) with a collected `Err()` so an app can validate every knob
and report all the bad ones at once.

Note the deliberate asymmetry with `netpath.Options`: netpath options are pure
data (registry-safe names), while `app.Options` carries **live components** —
the agent or embedder resolves names into a Network and an OffsetProvider
first, then builds. The pure-data description of an app is the `FlowSpec`.

### Registries

Apps self-register in `init` (ADR-0006), keyed by name:

```go
app.ClientRegistry.Register("voip", NewClient)
app.ServerRegistry.Register("voip", NewServer)
```

`components.Default()` exposes them as `Components.AppClients` /
`Components.AppServers`, which is what the agent builds from. The registered
names are wire-visible identifiers — they travel as `FlowSpec.app`, come back
in `CapabilitiesResponse.apps` (and the per-side `apps_client`/`apps_server`),
and double as the telemetry snapshot kinds. Never rename one.

### Placement on the wire

Two flow roles carry apps
([ADR-0024](https://github.com/bgrewell/loom/blob/main/DECISIONS.md#adr-0024--new-app_clientapp_server-flow-roles-not-a-responder-selector)
records why they're new roles, not a responder selector):

1. The controller (or embedder) configures `FLOW_ROLE_APP_SERVER` on the
   destination agent — `FlowSpec.app` names the engine, `network`/`local`
   select the netpath — and gets the server's bound `data_port` back.
2. It configures `FLOW_ROLE_APP_CLIENT` on the source agent with
   `target = server:data_port`, and starts both at one shared gate so the two
   ends' telemetry intervals stay aligned.

Both ends are checked against the agent's advertised `apps`/`networks` first
(the version-skew gate), so an old agent produces an actionable refusal, not a
confusing mid-placement error. App server flows must be **duration-bounded** —
orphan protection: a server whose controller crashed must not hold its port
forever — and the agent enforces the bound itself rather than trusting the
engine.

In a scenario, all of that is one event; the kind *is* the app name (with one
mapping: `video` is client-only, so its far end is placed as the `http`
origin — see the video example below):

```yaml
timeline:
  - name: a-call
    flow: { kind: voip, codec: g711, duration: 60s }
    from: phone
    to:   pbx
    start: 0s
```

App kinds accept only a duration bound (`stop.after` or the flow's `duration`
param); `stop.count`/`stop.volume` are refused up front rather than silently
dropped.

### Telemetry: quality snapshots

Byte/packet counters aren't the point of an app — quality is. An engine with
quality numbers implements `metrics.Source`, and the agent's telemetry
streamer discovers it **by type assertion** at boundaries (the `flowTCPInfo`
pattern: a capability, not a wider interface). The snapshot rides
`TelemetrySample.app` as a typed `AppMetrics` (`voip`/`http`/`video` oneof),
and the controller folds it into flow samples that observers render — the CLI
prints per-interval MOS lines for a voip flow with zero extra wiring.

Two reads, two meanings — this matters when consuming snapshots yourself:

- `Metrics()` **closes an observation interval**: interval loss %, interval
  percentiles. The agent calls it exactly once per boundary and fans the
  result out to all telemetry subscribers.
- `CumulativeMetrics()` (an optional capability, discovered by assertion)
  reads the whole run *without* closing an interval — it's what final
  samples and end-of-run summaries use, so a 60-second call with mid-call
  loss doesn't get summarized by its last clean sliver.

Other optional capabilities follow the same discover-by-assertion style:
`io.Closer` (release an eagerly bound port when a flow is torn down between
Configure and Start) and the `http` origin's `CertificatePEM()` (below).

## Worked example: `voip`

`core/app/voip` runs a bidirectional RTP/RTCP media session — synthetic media
paced at the codec's ptime, RFC 3550 Appendix A receiver statistics, a
jitter-buffer discard model, RTCP SR/RR/SDES/XR/BYE on the randomized RFC 3550
§6.3 interval, and a live E-model MOS at every boundary. The scoring pipeline
has [its own page](quality.md).

Client params (all optional): `codec` (default `pcmu`; `g711`/`g711a` aliases
work), `ptime` (Go duration), `jb_ms` (jitter-buffer depth, default 40),
`handshake_timeout_ms` (default 5000), `direction`
(`sendrecv`/`sendonly`/`recvonly`). The server adds `port_min`/`port_max` — an
inclusive bind range for firewall determinism; omitted means an ephemeral even
port.

The rendezvous is **symmetric RTP with a latch**: the answerer locks onto the
first `(source address, SSRC)` pair that passes RTP validity and RFC 3550 A.1
probation (two in-order packets); everything else is dropped and counted as
stray. A caller that hears no return RTP or RTCP within the handshake timeout
fails with a typed error. The SIP app will replace the latch with explicit
SDP-negotiated addresses — the media engine won't change
([design note](design/sip-app.md)).

### Quick mode: `loom rtp`

The standalone proof point — two hosts, no agents, no controller:

```console
host-b$ loom rtp --answer
answering on 0.0.0.0:49152  codec pcmu  ptime 20ms  jb 40ms

host-a$ loom rtp --call host-b:49152 --codec g711 --duration 60s
[   1.0s] rx  MOS 4.41  R  93.2  jit   0.3ms  loss  0.00%  disc  0.00%  rtt   0.8ms  owd 0.4±0.4ms (rtt/2)
[   2.0s] tx  MOS 4.41  R  93.2  (remote XR)
...
```

Both ends print per-interval quality, and each folds the *peer's* MOS/R in
from RTCP XR — both directions visible from either terminal. Quick mode runs
without TimeSync, so one-way delay is RTT/2 with a matching error bar, always
labeled `rtt/2`. `--json` emits one object per interval per end for scripting;
`--port`/`--port-min`/`--port-max` pin the answerer's bind for firewalls.

## Worked example: `http`

`core/app/httpx` registers both sides of app `"http"`: a genuine stdlib
HTTP/1.1 + TLS 1.3 (+ optional HTTP/2) client, and **HTTPOrigin** — loom's own
far end for web and video flows. The client's `http.Transport` dials through
`Options.Network.DialContext` and the origin listens through
`Options.Network.Listen`, so the entire stdlib stack — connection pool, TLS,
h2 framing — rides whatever the netpath seam provides.

The client fetches objects in a loop: `url_path` (fixed path) or `object_size`
(loom's size grammar, scalar or range, for generated `/object/{bytes}` paths),
`objects` (0 = unbounded), `think` (inter-request think time), `tls`, `h2`,
`host`. Timings come from `net/http/httptrace` — connect and TLS handshake on
new connections, TTFB and full transfer per request — aggregated into
`metrics.HTTP` percentiles (p50/p95/p99) plus goodput.

The origin serves deterministic synthetic content: `/object/{bytes}` bodies
(deterministic in seed and path), and a generated HLS/DASH bitrate ladder
(`/media/{name}/manifest.m3u8|.mpd`, per-rung playlists, sized segments) —
which is exactly what the video engine plays. Knobs: `tls`, `h2`, `host`,
`ladder`, `seg_duration`, `segments`, `port_min`/`port_max`.

TLS posture
([ADR-0028](https://github.com/bgrewell/loom/blob/main/DECISIONS.md#adr-0028--httpx-tls-verification-stays-on-pin-the-origins-cert-dont-disable)):
the origin generates a self-signed cert and exposes it through
`CertificatePEM()`; the consumer carries it to the client as the `tls_ca`
param (base64 PEM — travels safely in the params map, no shared filesystem)
and **verification stays on**. `tls_insecure` exists as an explicit lab-only
opt-in, never the default path.

## Worked example: `video`

App `"video"` (`core/app/vidstream`) is **client-only**: the far end is the
`http` origin's generated HLS ladder, fetched over the same shared transport
as the `http` client — so `tls`/`h2`/`host`/`tls_ca`/`tls_insecure` behave
identically across both apps. It is a **player model, not a real decoder**
([ADR-0029](https://github.com/bgrewell/loom/blob/main/DECISIONS.md#adr-0029--vidstream-is-a-player-model-not-a-media-decoder)):
the player fetches the master manifest, adopts its ladder as the truth, and
downloads segments into a virtual buffer that a playhead drains in real time.
Playback starts when the buffer first reaches `start_threshold` (default 2s);
the player fetches ahead to `buffer_target` (12s); a buffer that empties while
playing is a **stall**, resumed at `rebuffer_target` (4s) — and running dry
after the last segment is completion, not a stall. Segment bytes are counted
and discarded, never decoded.

Two ABR policies pick the rung before each fetch (`abr` param):
`throughput` (default — a harmonic-domain moving average of per-segment
throughput, so a bandwidth collapse pulls the estimate down within a segment
or two) and `buffer` (BBA-style buffer-level thresholds). Neither policy
applies hysteresis (no switch-up margin or dwell time) — a deliberate model
simplification: an estimate hovering at a rung boundary can oscillate between
adjacent rungs, so the switch counters measure raw policy decisions, not
debounced player behavior — don't read them as path-change indicators on
their own. Remaining knobs: `url_name` (the origin media name, default
`stream`), `ladder` (an optional *expectation* checked against the manifest —
the manifest stays the truth), `seg_duration` (playhead-accounting override;
default the EXTINF values).

The QoE comes out as `metrics.Video`: startup time, stalls and total stall
time, rebuffer ratio (stall time over stall+play time, so an interval that is
all stall reads 1.0), buffer level, duration-weighted average bitrate, ladder
switches up/down — and `StallEvents` as timed gaps, so a stall aligns with
media gaps and external events (a handover, an outage) on one timeline. A
stall still open when the flow ends is closed at that instant, so it never
vanishes from the timeline.

In a scenario, `kind: video` does the pairing for you: the controller places
the `video` player on `from` and — because the player is client-only — an
`http` origin on `to` as the far end (the skew gate checks the destination
agent for the *http server*, the engine actually placed there). Flow params
travel verbatim to both ends, so one `ladder` both configures the origin and
doubles as the player's expectation:

```yaml
timeline:
  - name: binge
    flow: { kind: video, ladder: "240p:400k,720p:2500k", seg_duration: 4s, duration: 3m }
    from: phone
    to:   cdn
    start: 0s
```

Embedding the engines yourself? The same shape applies: pair the `video`
client with an `http` origin serving the ladder it should play.

## Writing your own app

The contract, as a checklist:

1. **Dial and listen only through `Options.Network`.** No `net.Dial`, no
   `net.Listen`. This is the one rule that makes your engine run everywhere.
2. Register a `NewClient`/`NewServer` factory in `init` under one lower-case
   name; document every `Params` key you honor on the factory's doc comment.
3. Servers bind in the factory so `Addr()` is valid before `Run`; implement
   `io.Closer` to release that bind if the flow is torn down before Start.
4. `Run(ctx)` must terminate on both ctx cancellation and socket close;
   `Counters()` exposes live byte/packet totals via `core/accounting`.
5. Got quality numbers? Implement `metrics.Source` (interval semantics) and
   `CumulativeMetrics` (whole-run), and add a snapshot type to `core/metrics`
   — its JSON field names are pinned by golden tests, so wire evolution is
   deliberate.
6. Honor `Options.Seed` for anything random, so runs reproduce.

## Where to next

- **[Voice quality scoring](quality.md)** — the G.107 pipeline behind the
  voip numbers.
- **[Clocks & one-way delay](clock-sync.md)** — where `owd_ms ± owd_err_ms`
  comes from and what the labels mean.
- **[Design: real application traffic](design/real-app-traffic.md)** — the
  full design, including the orbit embedding that drives it.
