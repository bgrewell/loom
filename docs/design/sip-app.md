# Design note: the `sip` app

**Status:** queued — the agreed next step after the RTP/RTCP media plane
(design: [real application traffic](real-app-traffic.md)); this is a design
note, not an implementation. It exists so the seam the media engine already
exposes doesn't drift before the app that consumes it lands.

## The premise: the negotiation is the only new part

`core/app/voip` was built SIP-shaped on purpose. `voip.MediaConfig` — codec
row, local/remote RTP addresses, SSRC, direction, jitter-buffer depth — **is
exactly the parameter set an SDP offer/answer produces**, and its doc comment
says so: the future sip app "negotiates these values on the wire and hands the
struct to `NewMediaSession` unchanged". That's the whole design constraint:

```
INVITE / 200 OK / ACK  (SDP offer-answer)
        │  negotiates
        ▼
voip.MediaConfig ──► voip.NewMediaSession(network, cfg, owd)   ← unchanged
        ▲
        │  replaces
symmetric-RTP latch (today's stand-in for the address exchange)
```

The `sip` app owns signaling only. Media, pacing, receiver statistics, the
jitter-buffer discard model, RTCP/XR, and G.107 scoring stay exactly where
they are — the media engine never learns SIP. The one behavioral change on the
media side is free: a non-zero `RemoteRTP` already selects caller mode, so
SDP-negotiated addresses simply replace the answerer latch as the rendezvous
mechanism (the latch remains for the latch-based `voip` app; nothing is
removed).

## Shape of the app

A client/server pair registered as app `"sip"`, alongside — not replacing —
`"voip"`:

- **Transport:** all signaling dials and listens through the injected
  `netpath.Network`, like every app (the single-seam rule, ADR-0023). **UDP
  first** (RFC 3261 §18 with the transaction retransmission timers; one UA
  pair's messages fit well under the MTU), **TCP later** — the seam makes
  that a transport selection, not a redesign. Media flows over a *second* socket pair from the
  same Network, exactly as `voip` does today.
- **Call flow (v1):** INVITE with SDP offer → 200 OK with answer → ACK →
  media runs as a `MediaSession` → **BYE / 200 OK teardown** from whichever
  end's flow ends first (mapping naturally onto the app plane's
  duration-bounded flows; the RTCP BYE the session already sends becomes the
  belt to SIP BYE's suspenders).
- **Negotiation scope:** exactly what `MediaConfig` can express — codec (from
  the `core/rtp/codec` table, offered as standard payload-type/`rtpmap`
  lines), ptime, direction (`sendrecv`/`sendonly`/`recvonly`), addresses and
  ports. `rtcp-mux` is offered and assumed (the session is mux-only today); a
  refusal is a documented v1 limitation, not a hidden failure.
- **Server side (UAS):** binds the signaling port (inside
  `port_min`/`port_max` for firewall determinism, the established pattern),
  answers the offer by intersecting it with its own codec table, builds the
  answerer-mode `MediaSession` from the negotiated config. `Addr()` reports
  the signaling port as the flow's `data_port`.
- **Metrics:** the sip app adds signaling timings (INVITE→200 setup time,
  the SIP analog of TTFB) but the quality snapshot stays `metrics.VoIP`,
  produced by the same session — one results plane, no parallel VoIP metrics
  type.

## Out of scope for v1

Direct UA↔UA only, matching how every other loom app pair works
(client/server placed by the controller, versions gated by capabilities):

- **No registrars, no proxies, no redirects** — the controller already knows
  both endpoints; REGISTER solves a discovery problem loom doesn't have.
- **No digest auth / TLS signaling (SIPS)** — loom's control plane already
  authenticates who may configure flows (ADR-0014); signaling security
  becomes interesting only when a real proxy enters the picture.
- **No re-INVITE / mid-call renegotiation, no forking, no early media.**

Each exclusion is a scope decision, not an architectural one: none of them
touch the `MediaConfig` seam.

## The issue set it decomposes into

1. **`core/sdp`** — minimal SDP offer/answer: marshal/parse of the session
   and media descriptions `MediaConfig` needs (c=/m=/a=rtpmap/a=ptime/
   a=sendrecv family, a=rtcp-mux), plus the intersection rule. Pure data,
   golden-tested against captured offers.
2. **`core/sip`** — message codec + UA transaction cores (INVITE client/server
   transactions, non-INVITE for BYE; RFC 3261 timers over UDP), transport via
   `netpath.Network`. No dialog reuse beyond one call.
3. **`core/app/sip`** — the app pair: registries, params (`codec`, `ptime`,
   `jb_ms`, `port_min`/`port_max`, plus `sip_port`), MediaConfig handoff to
   `NewMediaSession`, BYE teardown, `metrics.VoIP` passthrough + setup-time
   fields.
4. **Wire + controller** — `"sip"` in the app registries/capabilities (the
   skew gate handles old agents), scenario kind `sip`, observer line reuse.
5. **Interop acceptance** — a loom UA answering a stock softphone's INVITE
   (and vice versa) with the G.711 stream decoding in Wireshark; the latch
   path's Wireshark cross-check extended to the signaled path.

That ordering keeps every PR independently testable — SDP and SIP cores are
pure packages with spec-vector tests before any app exists, the same way
`core/rtp` landed before `core/app/voip`.
