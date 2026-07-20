# Real application traffic

loom today emulates application *shapes*: `core/emul` behavior scripts replay an
app's size/think-time pattern over a raw transport
([DESIGN.md §10](https://github.com/bgrewell/loom/blob/main/DESIGN.md#10-traffic-emulation)).
This design adds the next tier: **wire-true application traffic** that loom
generates, answers, and scores itself — real RTP/RTCP voice with ITU-T G.107
MOS, real HTTP/1.1 + TLS (+h2), and an ABR video player — all riding any
datapath through one new seam, `netpath.Network`.

The first embedder driving these requirements is
[orbit](https://github.com/grewelltech/orbit), a 5G RAN/UE emulator that runs
loom's app clients over a GTP-U datapath and a **stock `loomd`** as the far end;
the canonical cross-repo design (including orbit's demux/netstack integration)
lives in that repo at `docs/design/real-app-traffic.md`. Everything on this page
is a loom feature in its own right: each capability lands loom-first, is
demoable with zero orbit involvement (`loom rtp --call` / `--answer` over any
two hosts), and follows the house rules — registry components with pure-data
options (ADR-0006/0022), additive wire changes (ADR-0021), tests with the code
(ADR-0015).

Related ADRs: [ADR-0023..0027](https://github.com/bgrewell/loom/blob/main/DECISIONS.md)
(netpath seam, app roles, E-model provenance, gVisor isolation, OWD labeling).

## 1. Shape of the addition

```
app.Client / app.Server         core/app/{voip,httpx,vidstream}      ← protocol engines
   │ net.Conn / net.PacketConn
netpath.Network                 core/netpath {"host","dgram",memory} + core/netstack
   │ frames (raw L3)
datapath.Tx/RxDatapath          core/datapath (contract unchanged, +RawL3 cap)
measurement                     core/rtp, core/rtp/rtcp, core/rtp/codec,
                                core/quality/{emodel,gilbert}, core/owd, core/metrics
coordination                    control/ (agent app roles), controller/ (placement+telemetry)
```

**One seam rule.** There is exactly one connection-factory abstraction,
`netpath.Network`. VoIP, HTTP, video, and the future SIP UA all dial/listen
through it. No `media.Transport`, no separate emul `Dialer`/`Listener` funcs —
`core/emul/reqresp` is refactored onto `netpath.Network` too, retiring its
concrete `net.Dial`/`net.Listen` (the defect that makes reqresp untunnelable
today). `core/emul` scripts themselves remain *shape-only* by design; the
wire-true engines live under `core/app`, not under emulation names (see §9).

## 2. `core/netpath` — the socket-semantics seam (NEW)

```go
// Package netpath provides connection-oriented network access (net.Conn /
// net.PacketConn semantics) as an injectable component: kernel stack,
// UDP-over-datapath, gVisor-over-datapath, or in-memory test loopback.
package netpath

type Network interface {
    Name() string
    DialContext(ctx context.Context, network, address string) (net.Conn, error)
    ListenPacket(network, address string) (net.PacketConn, error)
    Listen(network, address string) (net.Listener, error)
    Close() error
}

// Options are PURE DATA (registry-safe, ADR-0006 pattern). Embedders that
// construct datapaths out-of-band (orbit) do NOT go through the registry —
// they call the direct constructors below.  [fix: no live instances in Options]
type Options struct {
    Local        netip.Addr // source addr for datapath-backed networks; optional bind for "host"
    MTU          int
    TxDatapath   string
    RxDatapath   string
    DatapathOpts datapath.Options
}

func Host(local netip.Addr) Network                 // kernel stack, default
func Memory() (a, b Network)                        // paired in-memory nets for tests
```

`Host` is the default (kernel stack, what a stock `loomd` far end uses);
`Memory` gives paired in-memory networks so full app sessions run in CI with no
NICs, in the spirit of the `memory` datapath (ADR-0008/0015).

### 2.1 `core/netpath/dgram` — UDP with real headers over raw-L3 datapaths

```go
// Package dgram: UDP-only Network encoding real IPv4+UDP headers (checksums
// included) into frames of a raw-L3 datapath. Generalizes orbit's
// BuildUDPPacket into loom. Dial/Listen("tcp") return ErrTCPUnsupported.
package dgram // core/netpath/dgram

// New is the embedder constructor (live datapaths).
func New(tx datapath.TxDatapath, rx datapath.RxDatapath, local netip.Addr, mtu int) (netpath.Network, error)
// FromOptions is the registry factory (resolves names via Components).
func FromOptions(c *components.Components, o netpath.Options) (netpath.Network, error)
```

`dgram` is the lightweight path: UDP apps (VoIP at fleet scale) never pay a
userspace TCP stack's cost. Contract tests run over the memory datapath.

### 2.2 `core/datapath` — one additive capability

```go
type Capabilities struct {
    RawL2              bool
    RawL3              bool   // NEW: frames are complete IP packets
    HardwareTimestamps bool
    MaxPPS             uint64
}
```

`RawL3` marks frame payloads as complete IP packets so L3-consuming components
(`dgram`, `netstack`) can validate their backends. Built-in datapaths are
updated accordingly. This is the only change to the datapath contract.

## 3. `core/netstack` — gVisor TCP/IP as a netpath backend

```go
// Package netstack wraps gvisor.dev/gvisor/pkg/tcpip (pinned release, pure Go,
// no NET_ADMIN/TUN/netns). ONE Stack hosts MANY local addresses (all UEs of a
// gNB); per-UE isolation comes from per-connection source binding.
package netstack

type Config struct {
    MTU               int    // inner-IP MTU; orbit passes 1400 (1500 − outer IP 20 − UDP 8 − GTP-U 8..16 − slack)
    CongestionControl string // "cubic" (default) | "reno"; SACK + RACK enabled
}

type Stack struct{ /* *stack.Stack + dpEndpoint */ }

func New(cfg Config, tx datapath.TxDatapath, rx datapath.RxDatapath) (*Stack, error) // tx/rx must advertise RawL3
func (s *Stack) AddAddress(a netip.Addr) error     // UE attach
func (s *Stack) RemoveAddress(a netip.Addr) error  // UE release
// Network returns a per-UE source-bound netpath.Network view: DialContext binds
// the given local address (gonet.DialTCPWithBind), Listen binds on it. Closing
// a view does not close the Stack.
func (s *Stack) Network(local netip.Addr) netpath.Network
func (s *Stack) Close() error
```

Integration: `dpEndpoint` implements `stack.LinkEndpoint` directly over loom's
frame contract (no `channel.Endpoint`, avoiding one copy per packet):
`WritePackets → TxReserve/copy/TxCommit`; one RX goroutine loops
`RxPoll(64) → InjectInbound(ipv4/ipv6) → RxRelease`. Pure L3 endpoint (no link
addr, `CapabilityNone`), matching tunneled inner traffic.

Dependency posture (ADR-0026): gVisor is a large module with internal API
churn, so its imports are isolated in this one package, pinned at a tested
release; a `loom_nonetstack` build tag stubs it for minimal agents (same
build-tag isolation the heavy datapaths use). One multi-address `Stack` serves
many local addresses — hundreds of endpoints on one stack, never one stack per
address. Before any TCP-derived number is claimed, a netstack-vs-kernel
benchmark delta is published, and a sender-side timestamp audit quantifies
userspace-stack scheduling jitter separately so it is never silently attributed
to the network under test.

## 4. `core/rtp` — RFC 3550/3551/7587, algorithm-mandated

```go
package rtp

type Header struct {
    Padding, Extension, Marker bool
    PayloadType    uint8
    SequenceNumber uint16
    Timestamp, SSRC uint32
    CSRC           []uint32
}
func (h *Header) MarshalTo(b []byte) (int, error)                  // 12 + 4·len(CSRC)
func ParseHeader(b []byte) (h Header, payloadOffset int, err error)

// Packetizer: SSRC, initial seq and initial timestamp from crypto/rand
// (RFC 3550 §5.1). Timestamp advances by SamplesPerPacket on the MEDIA clock —
// never derived from time.Now() (wall-clock stamping makes receiver jitter
// measure the sender's scheduler). Marker set on first packet of a talkspurt.
type Packetizer struct{ /* codec, ssrc, seq, ts */ }
func NewPacketizer(c codec.Codec) *Packetizer
func (p *Packetizer) SSRC() uint32
func (p *Packetizer) Next(buf, payload []byte) (n int)

// PayloadSource: G.711 = band-limited synthetic speech (decodes/plays in
// Wireshark); Opus = valid TOC byte (20ms SILK-WB/CELT config) + pseudo-random
// body at the CBR target — "wire-format-true, content-synthetic", documented.
type PayloadSource interface{ Fill(buf []byte, pktIndex uint64) int }
func NewG711Source(law string) PayloadSource
func NewOpusSource(bitrateBps int) PayloadSource

// ReceiverStats implements RFC 3550 Appendix A EXACTLY (mandated in pkg docs,
// pinned by spec-vector tests):
//  A.1: 16→32-bit seq extension with cycle counting; MIN_SEQUENTIAL=2
//       probation; MAX_DROPOUT=3000; MAX_MISORDER=100; big jumps re-init_seq.
//  A.3: expected = ext_max − base + 1; lost signed, clamped to 24-bit
//       [−0x800000, 0x7FFFFF] only on the wire (negative under duplication).
//  Fraction lost: PER-INTERVAL, 8-bit fixed point, 0 if negative.
//  A.8: transit D in RTP TIMESTAMP UNITS; J += |D| − ((J+8)>>4) on 16× state;
//       exported raw (RR field) and ms (J/clockRate·1000); equal-ts packets excluded.
type ReceiverStats struct{ /* per A.1 source struct */ }
func NewReceiverStats(clockRate uint32) *ReceiverStats
func (s *ReceiverStats) Observe(h Header, payloadLen int, arrival time.Time)
func (s *ReceiverStats) Report() ReportBlockData   // feeds RTCP RR
func (s *ReceiverStats) Interval() RxSnapshot      // delta since last call
func (s *ReceiverStats) Cumulative() RxSnapshot

type RxSnapshot struct {
    Received, Duplicates, Reordered uint64
    Expected                        uint64
    CumulativeLost                  int64
    FractionLost                    float64
    ExtHighestSeq, JitterTicks      uint32
    JitterMs                        float64
    MaxGap                          time.Duration // longest interarrival gap (media-gap primitive)
    MediaGaps                       []Gap          // gap opens after >3·ptime silence
}
type Gap struct{ Start, End time.Time; PacketsLost uint32 }
```

The algorithm mandates above are not implementation hints — they are the
contract, pinned by spec-vector tests and a "naive implementations" checklist
in the package docs. Wire-realism acceptance: a generated pcap must decode
fully in Wireshark (correct PT/SSRC/seq/ts cadence, G.711 audio playable), and
Wireshark's RTP stream-analysis jitter/loss must match loom's own numbers.

### 4.1 `core/rtp/codec` — codec table

```go
package codec // core/rtp/codec

type Codec struct {
    Name        string        // "pcmu","pcma","g729","opus"
    PayloadType uint8         // 0, 8, 18 static; opus dynamic (default 111)
    ClockRate   uint32        // 8000; opus ALWAYS 48000 (RFC 7587 §4.1)
    Channels    uint8
    Ptime       time.Duration // default 20ms
    PayloadBytes     func(ptime time.Duration) int     // pcmu@20ms=160; g729@20ms=20
    SamplesPerPacket func(ptime time.Duration) uint32  // opus@20ms = 960 (48kHz clock)
    FrameLookahead   time.Duration // codec algorithmic delay: g711 0.25ms, g729 15ms, opus 26.5ms
    Ie, Bpl          float64       // G.113 App. I (g711 Bpl PLC-dependent: 25.1 PLC on [default], 4.3 off)
    Wideband         bool          // → G.107.1 scoring, IeWB/BplWB fields used
    IeWB, BplWB      float64
}
func ByName(name string) (Codec, error)
func Register(c Codec)
```

Opus impairment rows are provisional non-ITU values, documented as such and
overridable via `codec.Register`.

## 5. `core/rtp/rtcp` — SR/RR/SDES/BYE + XR, timing per §6.3/A.7

```go
package rtcp

type Packet interface{ AppendTo(b []byte) []byte }
type ReportBlock struct { SSRC uint32; FractionLost uint8; CumulativeLost int32 // 24-bit on wire
    ExtHighestSeq, Jitter, LSR, DLSR uint32 }
type SenderReport struct { SSRC uint32; NTPSec, NTPFrac, RTPTime uint32
    PacketCount, OctetCount uint32; Reports []ReportBlock }
type ReceiverReport struct { SSRC uint32; Reports []ReportBlock }
type SDES struct{ Chunks []SDESChunk }              // CNAME mandatory in every compound (§6.5)
type Bye struct{ SSRCs []uint32; Reason string }

// RFC 3611 XR blocks: BT=4 Receiver Reference Time, BT=5 DLRR (non-sender RTT),
// BT=7 VoIP Metrics (loss/discard/burst/gap densities Gmin=16, delays, R/MOS ×10).
type XRReceiverRefTime struct{ NTPSec, NTPFrac uint32 }
type XRDLRR struct{ Items []DLRRItem }
type XRVoIPMetrics struct { SSRC uint32
    LossRate, DiscardRate, BurstDensity, GapDensity uint8      // /256 fixed point
    BurstDuration, GapDuration, RoundTripDelay, EndSystemDelay uint16 // ms
    Gmin, RFactor, ExtRFactor, MOSLQ, MOSCQ uint8              // MOS ×10, 127 = unavailable
    JBNominal, JBMaximum, JBAbsMax uint16 }

func MarshalCompound(pkts ...Packet) ([]byte, error) // enforces SR/RR-first + SDES/CNAME
func ParseCompound(b []byte) ([]Packet, error)
func IsRTCP(b []byte) bool                            // RFC 5761 rtcp-mux classification

// NTP discipline (pinned by tests): 1900 epoch (Unix + 2208988800);
// frac = nanos·2^32/1e9; LSR = middle 32 bits of SR NTP; DLSR in 1/65536 s.
// RTT at sender = A − LSR − DLSR, ALL in 16.16 fixed point, converted last.
func NTPNow(t time.Time) (sec, frac uint32)
func RTTFromReport(arrival time.Time, rb ReportBlock) (time.Duration, bool)

// Interval implements RFC 3550 §6.3/A.7: Td = max(Tmin, n·C), randomized
// [0.5,1.5)·Td / 1.21828, 5% bandwidth share, 75/25 sender/receiver split,
// reconsideration. Prevents fleet-mode RTCP sync storms. Tmin default 5s.
type Interval struct{ SessionBW float64; Members, Senders int; WeSent, Initial bool; Tmin time.Duration }
func (iv *Interval) Next(rng *rand.Rand) time.Duration
```

SR's `RTPTime` maps the same sampling instant as its NTP timestamp (media clock
anchored to wall clock at session start) — receivers use SR pairs for clock
mapping. The randomized `Interval` scheduler is what keeps thousands of
concurrent calls from synchronizing their RTCP transmissions in fleet runs.

## 6. `core/quality` — burst loss and MOS

### 6.1 `core/quality/gilbert` — burst-loss estimator

```go
package gilbert

// Estimator fits an online 2-state Markov (Gilbert) loss model from the
// extended-seq loss/receive run-lengths: p = P(loss|prev recv), q = P(recv|prev loss).
// BurstR = 1/(p+q); 1 = random, >1 = bursty. Also emits RFC 3611 burst/gap
// density and duration with Gmin=16. ONE estimator shared by XR VoIP-metrics
// emission and the E-model's Ie,eff.
type Estimator struct{ /* run-length state */ }
func New(gmin int) *Estimator
func (e *Estimator) Observe(lost bool, at time.Time)
func (e *Estimator) Metrics() Metrics
type Metrics struct {
    P, Q, BurstR float64 // BurstR clamped ≥ 1
    BurstDensity, GapDensity float64
    BurstDuration, GapDuration time.Duration
}
```

### 6.2 `core/quality/emodel` — ITU-T G.107 / G.107.1, auditable

```go
package emodel

type Config struct {
    Codec    codec.Codec
    Wideband bool    // G.107.1 (R scale 0..129, own Idd coefficients + own R→MOS map)
    A        float64 // advantage factor, default 0 (do not hide impairment)
}
// Input semantics are EXPLICIT:
//   Ta  = network OWD + jitter-buffer nominal + codec frame+lookahead delay
//         (helper ComposeTa below; feeding raw OWD understates Id).
//   Ppl = percent (0..100) INCLUDING jitter-buffer discards (RFC 3611 discard
//         semantics) — this is what makes delay spikes hurt MOS.
type Input struct {
    Ta     time.Duration
    Ppl    float64
    BurstR float64 // from gilbert; clamped ≥ 1
}
type Components struct{ Ro, Is, Idte, Idle, Idd, Id, Ie, IeEff, A, R float64 } // audit breakdown
type Result struct{ R, MOSCQ float64; Method string /* "g107"|"g107.1" */; C Components }

func Score(cfg Config, in Input) (Result, error)
func ComposeTa(networkOWD, jbNominal time.Duration, c codec.Codec) time.Duration
func IeEff(ie, bpl, ppl, burstR float64) float64 // Ie + (95−Ie)·Ppl/(Ppl/BurstR + Bpl), Ppl in PERCENT
func MOSFromR(r float64) float64                  // G.107 Annex B; 1 below 0, 4.5 above 100
func MOSFromRWB(r float64) float64                // G.107.1 mapping on the 0..129 scale (NOT the NB polynomial)
```

**Mandated math** (in package docs + golden tests, ADR-0025): full default
formulas so zero-impairment R = 93.2 ± 0.01 (Ro ≈ 94.77, Is ≈ 1.41 computed,
not constants); `Id = Idte + Idle + Idd` with `T = Ta`, `Tr = 2·Ta`
(symmetric-path default; Ta/T/Tr never conflated); **Idd = 0 for Ta ≤ 100 ms**,
else `X = log10(Ta/100)/log10 2`,
`Idd = 25·[(1+X⁶)^(1/6) − 3·(1+(X/3)⁶)^(1/6) + 2]` — no FiDO2011 curve fit
anywhere. Golden tests pin the G.107 Table 4 verification examples and the
R→MOS reference table. A subtly wrong MOS is the worst failure mode for a
measurement tool — plausible numbers with no error to notice — which is why the
formulas are mandated, the `Components` breakdown is exposed in every result,
and live runs are cross-checked against Wireshark's RTP stream analysis.

## 7. `core/owd` — one-way delay with honest error bars

```go
package owd

type Method int
const ( Synced Method = iota; RTTHalf; AssumeSynced )

type Estimate struct {
    Value    time.Duration
    ErrBound time.Duration // half-width: min-filtered sync delay/2 + drift residual
    Method   Method
    Valid    bool
}
type OffsetProvider interface {
    Offset() (offset, errBound time.Duration, ok bool) // remote − local
}

// Tracker turns repeated four-timestamp exchanges (core/timesync.Sample) into
// a filtered offset with drift: minimum-delay sample per window (NTP
// clock-filter style), linear offset(t) fit over the last N windows,
// ErrBound = residual + delay/2. Fed by whoever runs the exchanges (orbit's
// engine or the loom controller loop) over a SYMMETRIC path (mgmt network,
// never through the tunnel).
type Tracker struct{ /* windows, fit */ }
func NewTracker(window time.Duration, n int) *Tracker
func (t *Tracker) Feed(s timesync.Sample, at time.Time)
func (t *Tracker) Offset() (offset, errBound time.Duration, ok bool) // satisfies OffsetProvider
```

This builds on the existing `TimeSync` service and the ADR-0010 seams; hardware
timestamps later slot into `Frame.Meta` (ADR-0010/0020) with no API change.

### 7.1 Clock-sync methodology ladder

Three tiers, and every OWD/Ta-derived number is labeled with `Method` +
`ErrBound` end-to-end (proto, CLI, Prometheus) — ADR-0027:

1. **timesync (default when a control channel exists):** the embedder or
   controller runs loomd's existing `TimeSync` RPC every ~10 s **over the
   symmetric management network, never through the data path under test**
   (asymmetry would poison the offset); `owd.Tracker` min-delay-filters and
   drift-fits; sub-ms ErrBound on a testbed LAN — ample for Id (knee at
   100 ms).
2. **rtt/2 fallback:** no control channel ⇒ OWD ≈ RTT/2 from LSR/DLSR with
   ErrBound = RTT/2 ("could be anywhere"), never silently presented as
   measured.
3. **assume-synced:** operator asserts NTP/PTP with a declared max error.

When ErrBound exceeds a threshold, the E-model input clamps to the RTT/2 tier
and says so.

## 8. `core/app` — application framework

```go
package app

type Options struct {
    Params  map[string]string   // codec, ptime, jb_ms, objects, ladder, port_min/port_max…
    Seed    int64
    MTU     int
    Network netpath.Network     // resolved by agent (registry) or embedder (direct)
    Target  string              // client side: server host:port
    OWD     owd.OffsetProvider  // nil ⇒ RTT/2 fallback, labeled
}

// Client and Server are flow.Runners: the agent's existing flowManager
// lifecycle (Configure/Arm/Start/Stop/Destroy, panic containment, telemetry
// boundaries) applies unchanged.
type Client interface {
    Name() string
    Run(ctx context.Context) error
    Counters() *accounting.Counters
}
type Server interface {
    Name() string
    Run(ctx context.Context) error
    Counters() *accounting.Counters
    Addr() netip.AddrPort // bound addr → Configure's data_port (Receiver.Port() pattern)
}
```

`core/components.Components` gains three additive registries (ADR-0022
pattern):

```go
type Components struct {
    // …existing five…
    Networks   *registry.Registry[netpath.Network, netpath.Options]
    AppClients *registry.Registry[app.Client, app.Options]
    AppServers *registry.Registry[app.Server, app.Options]
}
```

## 9. New flow roles: `APP_CLIENT` / `APP_SERVER` (and the rejected alternative)

New `FLOW_ROLE_APP_CLIENT=6` / `FLOW_ROLE_APP_SERVER=7` rather than overloading
`RESPONDER` with an emulation-name selector (ADR-0024). Rationale:

- `core/emul` is documented and implemented as *shape-only* carriage (mode.go);
  housing a wire-true protocol engine under an emulation name blurs loom's own
  taxonomy.
- RESPONDER/REQUESTER semantics are coupled to the reqresp transport field and
  BehaviorScript; apps are bidirectional, have their own metrics plane, and
  dispatch on `FlowSpec.app`.
- The reflector's `Unimplemented` arm stays untouched.

**Considered and rejected:** reusing `FLOW_ROLE_RESPONDER` with a selector
param naming the app ("start responder, emulation=voip"). It avoids two enum
values, but it couples every future app to reqresp's transport/BehaviorScript
plumbing, misfiles wire-true engines under the shape-only emulation taxonomy,
and gives app metrics no natural home in telemetry. The two additive enum
values are the cheaper long-term cost.

## 10. The app engines

### 10.1 `core/app/voip` — bidirectional RTP/RTCP with G.107 scoring

```go
package voip

// MediaConfig is exactly what SDP offer/answer will produce — the SIP seam.
// The future "sip" app negotiates and then hands this struct to NewMediaSession.
type MediaConfig struct {
    Codec          codec.Codec
    LocalRTP       netip.AddrPort // 0 port ⇒ ephemeral even port; RTCP-mux (RFC 5761) default on
    RemoteRTP      netip.AddrPort // zero ⇒ answerer mode: latch first valid source
    SSRC           uint32         // 0 = crypto/rand
    Direction      Direction      // SendRecv | SendOnly | RecvOnly
    JitterBufferMs int            // fixed playout model, default 40; late arrivals = discards → Ppl
    HandshakeTimeout time.Duration // default 5s; see latch rules below
}

type MediaSession struct{ /* tx pace @Ptime, rx loop, rtcp.Interval loop, gilbert, owd */ }
func NewMediaSession(n netpath.Network, cfg MediaConfig, o owd.OffsetProvider) (*MediaSession, error)
func (m *MediaSession) Run(ctx context.Context) error
func (m *MediaSession) Metrics() metrics.VoIP     // both directions + remote XR view

func NewClient(o app.Options) (app.Client, error) // registered "voip"
func NewServer(o app.Options) (app.Server, error) // registered "voip" (answerer)
```

**Rendezvous latch rules.** Answerer (server): binds inside
`port_min..port_max` when given (firewall determinism); latches the first
`(srcAddr, SSRC)` pair whose packets pass RTP validity + A.1 probation (2
in-order packets); all other sources are dropped and counted
(`stray_packets`). Caller (client): starts media immediately; if no return RTP
or RTCP arrives within `HandshakeTimeout`, `Run` returns a typed handshake
error (surfaced through telemetry). loom's auth token (ADR-0014) gates who may
Configure the server; far-end flows are always duration-bounded (orphan
protection). SIP replaces the latch with explicit SDP addresses later —
`MediaConfig` is deliberately SDP-shaped so the media engine is untouched when
the "sip" app arrives.

### 10.2 `core/app/httpx` — real HTTP/1.1 + TLS (+h2), client and origin

```go
package httpx // registered as "http"

// Client: real HTTP/1.1 + TLS (crypto/tls) + optional h2 (x/net/http2) via an
// http.Transport whose DialContext is the injected netpath.Network — the whole
// stdlib client stack rides the datapath. Per-request timings: connect, TLS
// handshake, TTFB, transfer; aggregates p50/p95/p99.
func NewClient(o app.Options) (app.Client, error) // params: url_path, objects, object_size, think, tls, h2, host
// Server ("HTTPOrigin"): net/http on the Network's Listener. Endpoints:
//   GET /object/{bytes}                          deterministic bodies
//   GET /media/{name}/manifest.m3u8|.mpd         generated ladder manifest
//   GET /media/{name}/{kbps}/seg{n}              segment sized kbps·segdur/8
// Self-signed TLS on demand; h2. This is the loom-owned far end per locked
// decision 1; nginx remains only an optional realism cross-check.
func NewServer(o app.Options) (app.Server, error)
```

### 10.3 `core/app/vidstream` — ABR player model

```go
package vidstream // registered as "video"; client-only (server is httpx)

// ABR player buffer model over httpx: fetch manifest, then segments; virtual
// playhead drains buffer in real time; stall = buffer 0 while playing; resume
// at rebuffer_target; throughput- or buffer-based ABR.
func NewClient(o app.Options) (app.Client, error) // params: ladder, seg_duration, buffer_target, abr
```

## 11. `core/metrics` — results plane

```go
package metrics

type Source interface{ Metrics() Snapshot }   // agent type-asserts at telemetry
type Snapshot interface{ Kind() string }      // boundaries, same pattern as flowTCPInfo

type VoIP struct {
    Codec string
    TxPackets, RxPackets, Lost, Duplicates, Reordered uint64
    LossPct, DiscardPct float64      // network loss vs jitter-buffer discard, both feeding Ppl
    JitterMs, RTTMs float64
    OWDMs, OWDErrMs float64          // error bar carried everywhere [fix]
    OWDMethod string                 // "timesync" | "rtt/2" | "assume-synced" | "none"
    BurstR, RFactor, MOSCQ float64
    EModel emodel.Components         // Ro/Is/Idte/Idle/Idd/Ie,eff audit breakdown [fix]
    RemoteRFactor, RemoteMOSCQ float64 // peer's view via RTCP XR
    MediaGaps []rtp.Gap
}
type HTTP struct {
    Requests, Errors uint64
    ConnectMs, TLSHandshakeMs, TTFBMsP50, TTFBMsP95, ObjectMsP95, GoodputMbps float64
}
type Video struct {
    SegmentsFetched, Stalls uint64
    StartupMs, StallTimeMs, RebufferRatio, BufferMs, AvgBitrateKbps float64
    RepSwitchesUp, RepSwitchesDown uint64
    StallEvents []rtp.Gap
}
```

The agent's telemetry streamer type-asserts `metrics.Source` at boundaries
exactly like `flowTCPInfo` today; the controller's `foldLocked` carries
AppMetrics into FlowSample/Aggregate and the Text/JSON observers render MOS/QoE
lines.

## 12. `loom.v1` wire changes (all additive; field numbers verified free)

Per ADR-0021, everything is additive against `control.proto`:

```proto
enum FlowRole {
  // existing 0..5 unchanged (REFLECTOR=3 stays Unimplemented)
  FLOW_ROLE_APP_CLIENT = 6;
  FLOW_ROLE_APP_SERVER = 7;
}
message FlowSpec {
  // existing 1..15, role = 21 unchanged
  string app     = 16; // "voip" | "http" | "video"
  string network = 17; // netpath network name; "" = "host"
  string local   = 18; // local addr for datapath-backed networks
}
message TelemetrySample {
  // existing 1..11 unchanged (final = 10, tcp = 11 — verified)
  AppMetrics app = 12;
}
message AppMetrics { oneof kind { VoipMetrics voip = 1; HttpMetrics http = 2; VideoMetrics video = 3; } }
message VoipMetrics { double mos_cq = 1; double r_factor = 2; double jitter_ms = 3;
  double loss_pct = 4; double discard_pct = 5; double burst_r = 6;
  double rtt_ms = 7; double owd_ms = 8; double owd_err_ms = 9; string owd_method = 10;
  uint64 rx_packets = 11; uint64 lost = 12; repeated MediaGap gaps = 13;
  double remote_mos_cq = 14; EModelBreakdown emodel = 15; }
message EModelBreakdown { double ro = 1; double is = 2; double idte = 3; double idle = 4;
  double idd = 5; double ie_eff = 6; }
message MediaGap { int64 start_unix_nanos = 1; int64 end_unix_nanos = 2; uint32 packets_lost = 3; }
message HttpMetrics { uint64 requests = 1; uint64 errors = 2; double ttfb_ms_p95 = 3;
  double goodput_mbps = 4; double tls_handshake_ms = 5; double connect_ms = 6; }
message VideoMetrics { uint64 stalls = 1; double stall_time_ms = 2; double rebuffer_ratio = 3;
  double buffer_ms = 4; double avg_bitrate_kbps = 5; double startup_ms = 6;
  repeated MediaGap stall_events = 7; }
message CapabilitiesResponse { /* existing 1..5 */ repeated string networks = 6; repeated string apps = 7; }
```

**Version-skew gate.** Every consumer (an embedder, the loom controller) checks
`CapabilitiesResponse.apps`/`networks` at provision time and fails fast with an
actionable error: `loomd at n6:9551 (v0.9.1) lacks app "voip"; run loom >=
v0.10`. All changes additive per ADR-0021, so mixed versions degrade to clean
refusals.

Agent wiring: `control/agent.go` gains `configureAppServer` (build a Network
from `Components.Networks` per `FlowSpec.network`, `AppServers.Build(app,
opts)`, return `Addr().Port()` as `data_port`) and `configureAppClient`.
Controller wiring: scenario `flow: {kind: voip|http|video}` places APP_SERVER
on `to` + APP_CLIENT on `from`, mirroring the existing responder/requester
placement.

## 13. The far end is a stock `loomd`

No new daemon. The far end of any app session is a **stock loomd agent**: loom
already provides lifecycle, auth (ADR-0014), TimeSync, and boundary-anchored
telemetry. `Configure(role=APP_SERVER, app="voip"|"http", network="host",
params{port_min,port_max,codec,…})` → server built and registered under
`flowManager`, `data_port` returned, flow duration-bounded (orphan protection
even if the driving side crashes). Both ends stream telemetry: the driving side
subscribes `StreamTelemetry` on the far-end loomd for the as-received series
while its local client supplies the other direction. The VoIP answerer latches
per §10.1; the HTTP/video far end is loom's own `httpx` HTTPOrigin (objects +
generated HLS/DASH ladder + TLS/h2) — nginx only as optional cross-validation.
This is what makes the capability independently useful: deploy loomd from the
normal release flow (`scripts/install.sh`, `loomd.service`), point anything at
it. In orbit's testbed this loomd sits on the N6 network behind the UPF, but
nothing about it is orbit-specific.

## 14. Quick mode: `loom rtp --call` / `--answer`

```
loom rtp --answer
loom rtp --call host:port --codec g711 --duration 60s
```

A standalone two-host VoIP demo in the iperf-esque spirit
([DESIGN.md §11](https://github.com/bgrewell/loom/blob/main/DESIGN.md#11-roles--topology)):
a 60 s call with live per-interval MOS/jitter/loss/RTT/OWD(method ± err) from
both ends, no controller, no scenario file, no embedder. This is the proof
point and dogfood path for the whole stack — it must decode cleanly in
Wireshark (RTP + RTCP SR/RR/XR) and refuse an old far end via the version-skew
gate.

## 15. Phased loom deliverables

Each phase is independently demoable and sized to 1–3 PRs. (The embedding
side — orbit's GTP-U demux, per-gNB netstack bridge, event correlation —
interleaves between these phases in its own repo, pinning loom by tag.)

1. **Measurement core** (loom-only): `core/rtp` (A.1/A.3/A.8-exact
   ReceiverStats, Packetizer, payload sources), `core/rtp/codec`,
   `core/rtp/rtcp` (SR/RR/SDES/BYE/XR + §6.3/A.7 interval + NTP discipline),
   `core/quality/gilbert`, `core/quality/emodel` (G.107 + G.107.1 with
   Components breakdown), `core/owd` Tracker — all pure packages with
   spec-vector and G.107 Table-4 golden tests. *Demo:* `go test ./core/rtp/...
   ./core/quality/... ./core/owd/...` green; a generated pcap decodes fully in
   Wireshark (stream-analysis jitter/loss matches loom's numbers; G.711 audio
   plays); emodel reproduces ITU reference R/MOS values incl. R=93.2 defaults.
2. **netpath seam + voip as a library** (loom-only): `core/netpath` (Network,
   host, memory, Networks registry, reqresp refactor ADR),
   `core/netpath/dgram` + `Capabilities.RawL3`, `core/app` (Client/Server/
   Options + AppClients/AppServers registries), `core/app/voip` MediaSession
   (latch rules, JB discard model, boundary MOS), `core/metrics` snapshots.
   *Demo:* a Go example runs a bidirectional G.711 call between two processes
   over the host network with live interval jitter/loss/RTT/MOS both
   directions; `tc netem` loss moves MOS per the G.113 curve, bursty vs random
   loss scored differently via BurstR; the same session runs over the memory
   network in CI.
3. **Agent/controller wiring + quick mode** (loom-only; tag v0.10): proto
   additions (APP_CLIENT/APP_SERVER roles, FlowSpec 16–18,
   TelemetrySample.app=12, CapabilitiesResponse networks/apps), agent
   `configureAppServer/Client` + metrics-attached telemetry, controller
   scenario kind `voip` + observers, `loom rtp --call/--answer`, version-skew
   gate, `port_min/max`, duration-bounded flows. *Demo:* stock loomd on one
   box, `loom rtp --call` from another — a 60 s G.711 call with per-interval
   MOS/jitter/loss/RTT/OWD(method ± err) from both ends; Wireshark decodes
   RTP + RTCP SR/RR/XR; an old loomd is refused with `lacks app "voip"; run
   loom >= v0.10`.
4. **netstack TCP + real HTTP/TLS** (loom v0.11): `core/netstack`
   (multi-address gVisor Stack, dpEndpoint over the frame contract,
   `Network(local)` views, benchmark + timestamp-audit harness); `core/app/httpx`
   client + HTTPOrigin server (TLS/h2, objects + segment endpoints);
   HttpMetrics end-to-end; published netstack-vs-kernel delta. *Demo:* HTTPS
   objects fetched from an HTTPOrigin through a datapath-backed network — real
   TCP SYN and TLS 1.3 ClientHello on the wire; TTFB/goodput/TLS-handshake
   metrics stream.
5. **Video QoE + fleet aggregation**: `core/app/vidstream` ABR player over
   httpx; VideoMetrics with stall events; per-cohort aggregate MOS/QoE
   (p5/p50/p95) in controller aggregation + Prometheus; ADR + design note for
   the "sip" app driving `voip.MediaConfig` (implementation next). *Demo:* a
   3-minute synthetic HLS-style ladder plays from the HTTPOrigin with a
   buffer-level timeline; fleet-scale runs hold mixed voip/web/video cohorts
   with no RTCP sync storms (§5's randomized interval).

## 16. Measurement pipeline at a glance

```
caller (app.Client "voip")                                answerer (loomd, app.Server "voip")
 Packetizer @ Ptime (media-clock ts, crypto/rand ids) ──► ReceiverStats.Observe(hdr, rxTime)
 ReceiverStats.Observe ◄── return media ───────────────── Packetizer (bidirectional call)
   │ per packet: A.1 ext-seq/probation → loss/dup/reorder; A.8 jitter (ts units);
   │ gilbert.Observe(lost) → p,q,BurstR + burst/gap densities; JB model → discards
   ├ RTT: RFC 3550 §6.4.1 A − LSR − DLSR (16.16 fixed point)   ◄── RTCP SR/RR/XR ──┐
   ├ OWD: arrival_local − (SR NTP send time + owd.Tracker offset), ± ErrBound       │
   └ per telemetry boundary:                                                        │
       Ppl = network loss% + JB discard%   BurstR from gilbert                      │
       Ta  = ComposeTa(OWD, JB nominal, codec frame+lookahead)                      │
       emodel.Score → {R, MOS-CQ, Components breakdown} → metrics.VoIP ─► TelemetrySample.app
 RTCP tx cadence: rtcp.Interval (§6.3/A.7 randomized); XR VoIP-metrics carries R/MOS on the wire
```
