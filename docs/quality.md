# Voice quality scoring

loom's voip engine doesn't report "loss 2%, jitter 12 ms" and leave the
interpretation to you — it scores the call the way the telephony world does,
with the **ITU-T G.107 E-model**, and reports an R-factor and a MOS alongside
the raw impairments. This page walks the pipeline end to end: what is
measured, how it becomes a score, and why you can trust (and audit) the
number.

The one-sentence version:

```
RTP arrival ──► rtp.ReceiverStats ──► loss% ┐
              (RFC 3550 Appendix A)         │
playout model ────────────► discard% ───────┼─► Ppl ┐
gilbert.Estimator ────────► BurstR ─────────────────┤
owd + jitter buffer + codec ─► emodel.ComposeTa ─► Ta ┼─► emodel.Score ─► R, MOS-CQ
                                                    ┘        + Components breakdown
```

## What MOS-CQ means

**MOS-CQE**, strictly: the *estimated conversational-quality* mean opinion
score, on the familiar 1–4.5 scale, derived from the E-model's transmission
rating factor **R** (0–100 narrowband, where 93.2 is the defaults-perfect
ceiling; 0–129 wideband). "Conversational" is the operative word: it includes
what delay does to a *conversation* — talker overlap, echo annoyance — not
just how clean the audio sounds. That's why a call with pristine audio and
350 ms of one-way delay scores poorly, and why loom refuses to compute MOS
from loss alone.

Rules of thumb: R ≥ 80 (MOS ≈ 4.0) is "satisfied", R around 70 (MOS ≈ 3.6)
"some users dissatisfied", below 60 you have a problem, below 50 nearly
everyone is dissatisfied.

## Stage 1: receiver statistics (`core/rtp`)

Every arriving RTP packet feeds `ReceiverStats.Observe`, which implements
RFC 3550 **Appendix A exactly** — not approximately:

- **A.1** — 16→32-bit sequence extension with cycle counting, two-packet
  probation before a source is believed, `MAX_DROPOUT`/`MAX_MISORDER`
  handling with the re-init handshake.
- **A.3** — `expected = extended_max − base + 1`; cumulative loss is
  **signed** (duplication legitimately drives it negative) and clamped to the
  24-bit wire range only at report time; fraction lost is **per-interval**,
  floored at zero.
- **A.8** — interarrival jitter on transit-time differences in **RTP
  timestamp units**, with the fixed-point estimator `J += |D| − ((J+8)>>4)`.

The sender side holds up its end of the bargain: the packetizer advances RTP
timestamps on the **media clock**, never from `time.Now()` — wall-clock
stamping would make receiver jitter measure the sender's scheduler instead of
the path. The `core/rtp` package docs carry a checklist of exactly these
naive-implementation mistakes, each pinned by a test, because
plausible-but-wrong statistics are the worst failure mode a measurement tool
has.

## Stage 2: the playout model — why discards count

A packet that arrives too late to play is as lost as one that never arrived.
The session models a **fixed playout point**: the first counted packet anchors
media-clock time, and every later packet's deadline is that anchor plus its
timestamp advance plus the jitter-buffer depth (`jb_ms`, default 40). Arrive
after the deadline and you're a **discard** in the RFC 3611 sense: counted,
fed to the burst estimator as a loss, and included in the E-model's loss
input.

This is the piece that makes delay *spikes* hurt MOS. Without discard
accounting, a 200 ms stall that arrives eventually is invisibly "free"; with
it, `Ppl = network loss% + discard%`, which is what a real endpoint's ear
experiences.

## Stage 3: burstiness (`core/quality/gilbert`)

Twenty losses scattered through a minute sound like crackle; twenty losses in
a row are a dropout. The E-model distinguishes them through **BurstR**, and
loom estimates it online with a two-state Markov (Gilbert) fit over the
per-slot loss/receive sequence: `p = P(loss | prev received)`,
`q = P(received | prev lost)`, `BurstR = 1/(p+q)` — 1 for random loss, larger
for bursty, clamped to ≥ 1.

The same estimator derives the RFC 3611 §4.7.2 burst/gap densities and
durations (Gmin = 16), implemented to the RFC's exact edge semantics. **One
estimator feeds both** the XR VoIP-metrics blocks on the wire and the
E-model's `Ie,eff` — the two consumers can never disagree about how bursty the
loss was.

## Stage 4: delay composition (`emodel.ComposeTa`)

The E-model wants **Ta, the absolute mouth-to-ear delay** — not the raw
network OWD. Feeding raw OWD understates the delay impairment and flatters
MOS. loom composes it explicitly:

```
Ta = network OWD + jitter-buffer nominal + ptime + encoder lookahead
```

The packetization interval covers frame buffering (frames fill *during* the
packetization wait — adding a separate frame time would double-count), and the
codec row supplies the encoder's algorithmic lookahead. Network OWD comes from
the [clock ladder](clock-sync.md), error bar and all. Delay starts hurting at
the G.107 knee: Idd is zero up to Ta = 100 ms and climbs steeply past it.

## Stage 5: the E-model itself (`core/quality/emodel`)

`emodel.Score` implements the **full default-parameter formulas** of G.107
(06/2015) and, for wideband codecs, G.107.1 (06/2019) — computed `Ro` and
`Is`, the three delay impairments `Idte`/`Idle`/`Idd` with Ta, T and Tr never
conflated, and the burst-adjusted `Ie,eff`. No curve fits, anywhere
([ADR-0025](https://github.com/bgrewell/loom/blob/main/DECISIONS.md#adr-0025--voice-quality-via-full-itu-t-g107g1071-e-model-not-curve-fits)
records why). Golden tests pin the G.107 Table 4 verification examples, the
Annex B R→MOS reference table, and the defaults-only rating R = 93.2 ± 0.01 —
if a formula drifts, CI catches it against the spec's own numbers.

Every result carries a **`Components` breakdown** — Ro, Is, Idte, Idle, Idd,
Ie,eff, A, R — end to end: it's in the `metrics.VoIP` snapshot, the proto, and
the JSON output. A disputed MOS decomposes term by term: *was that 3.1 the
delay's fault or the loss's?* is answerable from the telemetry, not a
forensics project. `Score` also returns an **error** for inputs outside the
model's domain rather than a plausible-looking number.

## The codec table (`core/rtp/codec`)

Codec rows carry the RTP identity and the G.113 impairment parameters the
E-model reads. Provenance, per row:

| Codec | PT | Clock | Ie | Bpl | Lookahead | Source |
|---|---|---|---|---|---|---|
| `pcmu` / `pcma` (G.711) | 0 / 8 | 8000 | 0 | 25.1 (PLC) | 0.25 ms | RFC 3551 §4.5.14/§6; G.113 App. I |
| `g729` (G.729-A + VAD) | 18 | 8000 | 11 | 19.0 | 5 ms | RFC 3551 §4.5.6; G.113 App. I Tables I.1/I.2 |
| `opus` (wideband) | 111 (dyn) | 48000 | 5* | 15* | 6.5 ms | RFC 7587; *provisional, see below* |

Details that trip people up, handled deliberately: the Opus RTP clock is
**always 48 kHz** (RFC 7587 §4.1) regardless of coded bandwidth, so 20 ms is
960 timestamp units; G.711's Bpl is PLC-dependent — 25.1 with concealment (the
default), 4.3 without (`codec.G711BplNoPLC`, register a copy of the row to
score a no-PLC receiver).

**The Opus rows are provisional.** ITU-T has published no G.113 Ie/Bpl (or
wideband Ie,wb/Bpl,wb) values for Opus. The seeded numbers (IeWB = 5,
BplWB = 15) are engineering placeholders, documented as such in the package —
treat Opus MOS as provisional, quote G.711 as the headline number, and
calibrate via `codec.Register`, which *replaces* rows by design. Wideband
rows score through G.107.1: its own 0–129 R scale and its own Annex A R→MOS
map — never the narrowband polynomial applied to a wideband R.

## On the wire: RTCP XR

The same numbers travel in-band. Each session emits compound RTCP on the
randomized RFC 3550 §6.3 interval (which is what prevents report
synchronization storms at fleet scale), including RFC 3611 XR blocks:
**Receiver Reference Time** (BT-4) and **DLRR** (BT-5) so even a non-sender
gets an RTT, and **VoIP Metrics** (BT-7) carrying loss/discard rates, burst
and gap density/duration (Gmin = 16), delays, and R/MOS (MOS ×10; 127 =
unavailable) — so the *peer's* view of your transmit direction comes back and
lands in the snapshot as `RemoteRFactor`/`RemoteMOSCQ`. Both ends of a call
are visible from either end.

Independent cross-check: the media is wire-format-true (the G.711 source is
even playable audio), so Wireshark's RTP stream analysis decodes a loom call
and its jitter/loss must agree with loom's own numbers — a live acceptance
test against a second implementation of the same specs.

## Reading the numbers

From `metrics.VoIP` (CLI, JSON, proto — same fields everywhere): `MOSCQ` and
`RFactor` with the `EModel` breakdown; `LossPct` + `DiscardPct` (their sum was
Ppl); `BurstR`; `JitterMs` (A.8); `RTTMs` (LSR/DLSR); `OWDMs ± OWDErrMs` with
`OWDMethod` — read [the clock page](clock-sync.md) before trusting an OWD; and
`MediaGaps`, the timed holes in arrival that line up with outages and
handovers. Interval reads score the interval; the end-of-run summary scores
the whole call (`CumulativeMetrics`), so a final number never reflects just
the last clean sliver.
