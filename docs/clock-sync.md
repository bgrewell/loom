# Clocks & one-way delay

Round-trip time needs one clock; **one-way delay needs two clocks that
agree** — and software clocks don't, by default, to anywhere near the
precision a delay measurement wants. Plenty of tools quietly print RTT/2 and
call it OWD. loom's position
([ADR-0027](https://github.com/bgrewell/loom/blob/main/DECISIONS.md#adr-0027--one-way-delay-is-always-labeled-with-method--error-bound)):
every OWD figure carries **the method that produced it and an error bound**,
end to end — proto, CLI, Prometheus, JSON. An unlabeled OWD number is a lie
waiting to happen.

```go
type Estimate struct {
    Value    time.Duration
    ErrBound time.Duration // true value believed within Value ± ErrBound
    Method   Method        // how both were obtained
    Valid    bool
}
```

## The ladder

Three tiers, from best to bluntest. The label travels with every derived
number — including MOS, since OWD feeds the E-model's delay impairment.

### `timesync` — measured offset

When a session has an `owd.OffsetProvider`, OWD is measured directly: each
RTCP sender report carries the sender's NTP-format send time, and
`OWD = arrival − (SR time − offset)`, with the offset (remote − local) coming
from the provider. The standard provider is **`owd.Tracker`**, fed by repeated
four-timestamp exchanges (`core/timesync`, RFC 5905 §8 style):

- **Minimum-delay filtering.** Per window (default 30 s), only the
  minimum-round-trip sample is kept — the NTP clock-filter insight (RFC 5905
  §10) that the least-delayed exchange is the least queuing-polluted one.
- **Drift fitting.** Over the minima of the last N windows (default 8, ≈ four
  minutes of history), a least-squares linear `offset(t)` fit — so a remote
  clock that drifts steadily is *tracked*, not smeared into the average.
- **An honest bound.** `ErrBound` = the fit's worst residual plus half the
  minimum observed round-trip delay — the irreducible asymmetry uncertainty
  of a four-timestamp exchange (an exchange's offset error is at most
  delay/2). On a testbed LAN this lands comfortably under a millisecond,
  which is ample: the E-model's delay impairment doesn't even wake up until
  100 ms.

### `rtt/2` — the labeled guess

No offset provider? The session falls back to half the RTCP-measured round
trip (LSR/DLSR arithmetic), with **ErrBound = RTT/2** — the delay could sit
anywhere in the round trip, and the bound says so. This is the honest version
of what many tools print as "OWD". It is never presented as measured; `loom
rtp` quick mode runs in this tier and labels every line.

### `assume-synced` — the operator's word

The escape hatch for infrastructure that *is* disciplined (NTP/PTP with known
quality): the operator asserts synchronization and declares a maximum error,
which becomes the bound. loom didn't measure it and the label says exactly
that. (`owd.Method` defines the tier; supply an `OffsetProvider` that reports
your declared bound.)

Before any estimate exists at all, snapshots say `owd_method: "none"` — never
a zero that could be mistaken for a very fast network.

## Why sync never rides the measured path

The four-timestamp exchange has one blind spot: **path asymmetry**. The
math assumes the outbound and return legs are equal; an asymmetric path
biases the offset by *half the asymmetry difference*, and no amount of
filtering can see it — the samples are self-consistent and wrong.

Now consider measuring a tunnel: if the sync exchanges rode the tunnel itself,
the tunnel's own asymmetry — the very thing under test — would silently bend
the clock offset, and OWD would come out flattered or inflated in exactly the
direction that hides the effect you're measuring. So the rule is absolute:
**time-sync exchanges run over a symmetric path that is not the path under
test** — in practice the management network the control plane already uses —
while the media rides the measured path. The `Tracker` docs carry the same
warning; it's a deployment invariant, not a tuning preference.

(Same reasoning, smaller print: feed the Tracker regularly — the bound covers
fitted drift, not drift accumulated after the last exchange.)

## What it looks like downstream

Every consumer shows the triple, not just the value:

```
owd 0.4±0.4ms (rtt/2)      ← loom rtp, human lines
"owd_ms": 12.3, "owd_err_ms": 0.6, "owd_method": "timesync"   ← JSON / proto
```

And because Ta (the E-model's delay input) is composed from OWD
([quality page](quality.md)), a MOS derived from an `rtt/2`-tier OWD is only
as trustworthy as that tier — the labels exist so you can decide whether a
number is evidence or an estimate. Hardware timestamps slot in later through
the datapath frame metadata (ADR-0010/0020) without changing any of this
plumbing: they just make tier 1 sharper.
