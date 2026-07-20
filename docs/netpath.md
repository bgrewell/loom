# The netpath seam

Real application engines (VoIP, HTTP/TLS, video) need sockets — `net.Conn` and
`net.PacketConn` semantics — not raw frames. **`core/netpath` is where they get
them**: a single connection-factory interface, `netpath.Network`, that every
app dials and listens through. Swap the Network and the same engine runs over
the kernel stack, an in-memory test fabric, or a tunnel's raw packet lane,
unchanged.

## The single-seam rule

There is exactly **one** connection-factory abstraction in loom
([ADR-0023](https://github.com/bgrewell/loom/blob/main/DECISIONS.md#adr-0023--one-connection-factory-seam-netpathnetwork)):

```go
type Network interface {
    Name() string
    DialContext(ctx context.Context, network, address string) (net.Conn, error)
    ListenPacket(network, address string) (net.PacketConn, error)
    Listen(network, address string) (net.Listener, error)
    Close() error
}
```

Nothing in `core/app` calls `net.Dial` or `net.Listen` directly — ever. The
rule exists because the alternative is what happens to every traffic tool
eventually: each protocol engine grows its own ad-hoc transport (`media.Transport`
here, a `Dialer` func there), and none of them can ride an injected datapath.
With one seam, a capability added to the seam — a new backend, per-packet
receive timestamps — reaches every engine at once, and an engine written today
runs over a backend written next year.

The arguments follow `net.Dial` conventions (`"tcp"`/`"udp"`, `"host:port"`).
A backend that doesn't support a network name returns an error matching the
`netpath.ErrUnsupportedNetwork` sentinel (via `errors.Is`), so callers can
distinguish "this backend can't do TCP" from "connection refused". Addresses
are numeric — the seam deliberately carries **no name resolution**; resolve at
the edge (the CLI does, in `loom rtp`).

## The backends

| Network | What it is | TCP | UDP | Needs |
|---|---|---|---|---|
| `host` | the kernel stack (the default) | yes | yes | nothing |
| `memory` | in-process test fabric, no kernel | yes | yes | nothing |
| `dgram` | real IPv4+UDP headers written into a raw-L3 datapath | no | yes | a datapath with `RawL3` |
| `netstack` | gVisor userspace TCP/IP over a raw-L3 datapath | yes | yes | a datapath with `RawL3`; not built with `loom_nonetstack` |

### `host` — the kernel stack

`netpath.Host(local)` wraps `net.Dialer`/`net.ListenConfig`. Given a valid,
non-unspecified `local` address it binds it as the source everywhere: dials
bind it as the dialer's local address, and listens substitute it when the bind
address leaves the host empty or unspecified (`":0"`, `"0.0.0.0:80"`). Pass the
zero `netip.Addr` for no binding. `Close` tears down nothing — the kernel owns
the sockets.

**Use it** whenever the OS network path *is* the path under test. It's the
default: a `FlowSpec` with an empty `network` field resolves to `host`.

### `memory` — the in-process fabric

`netpath.Memory()` returns a **connected pair** of Networks sharing one fabric:
a listener bound through either handle is reachable from both, so a test reads
naturally (client dials `a`, server listens on `b`). Streams are full-duplex
`net.Pipe`-backed conns; datagrams get real packet-conn semantics including
deadlines that wake blocked reads. Simplifications are deliberate and
documented on the constructor: one flat port namespace routed by port only,
addresses print as `mem:<port>`, a datagram to an unbound port vanishes
silently (as UDP does), and a full receive buffer drops.

**Use it** for CI and unit tests: no kernel, no NIC, no privileges, no flaky
ports. The whole VoIP media session runs over it in loom's own test suite.

### `dgram` — UDP with real headers over a packet lane

`core/netpath/dgram` is a UDP-only Network that encodes a complete,
checksum-correct IPv4+UDP packet into every frame of a raw-L3 datapath, and
validates/demultiplexes inbound frames to bound packet conns by destination
port. It generalizes "UDP over an injected packet lane" — e.g. the inner-IP
payload of a GTP-U tunnel — behind the standard seam.

Points worth knowing:

- **UDP only, IPv4 only** (IPv6 arrives with a later revision). Stream
  networks are refused with an error matching both `ErrUnsupportedNetwork`
  and `dgram.ErrTCPUnsupported`, so a caller can tell "pick a TCP-capable
  backend" apart from a typo. A userspace TCP stack is a different backend —
  that's `netstack`, below.
- **Wireshark-clean on the wire**: outbound IPv4 header and UDP pseudo-header
  checksums are always computed (a computed zero is sent as `0xFFFF` per
  RFC 768). Inbound packets that aren't IPv4, are fragments, aren't UDP, or
  fail a checksum are dropped and counted (`DropStats`).
- **Arrival timestamps are preserved**: every packet conn it returns
  implements `MetaConn`, whose `ReadFromMeta` also returns the frame's
  receive timestamp (ADR-0020 `Frame.Meta`) — the timestamps RTP jitter
  measurement wants, without a second clock read.

**Use it** for datagram apps (VoIP) over a tunneled path. It is deliberately
lightweight — header encode/decode per packet, one receive goroutine — so a
fleet of voice sessions never pays for a TCP stack it isn't using.

### `netstack` — userspace TCP/IP over a packet lane

`core/netstack` wraps gVisor's `pkg/tcpip` (pure Go — no NET_ADMIN, no TUN, no
netns) as a netpath backend, so TCP-based apps (HTTP/TLS, video) can ride a
raw-L3 packet lane the kernel never sees. The shape matters
([ADR-0026](https://github.com/bgrewell/loom/blob/main/DECISIONS.md#adr-0026--gvisor-isolated-in-corenetstack-behind-a-build-tag)):

- **One `Stack` hosts many local addresses** — never one stack per address.
  An embedder adds and removes addresses as endpoints attach and detach
  (`AddAddress`/`RemoveAddress`), and `Stack.Network(local)` returns a
  **source-bound view**: dials bind the view's address, listens bind on it,
  and closing a view closes only what was created through it — never the
  Stack.
- The link layer is implemented **directly over the frame contract**
  (`TxReserve`/`TxCommit`, `RxPoll`/`RxRelease`) with no intermediate
  `channel.Endpoint`, so there is no second copy per packet. It is a pure L3
  endpoint — no link addresses, no ARP — matching tunneled inner-IP traffic.
- Config is small: `MTU` (576..65535; pass the *inner* MTU when the lane is a
  tunnel payload) and `CongestionControl` (`cubic` default, or `reno`; SACK
  and RACK are always on). IPv4 first: IPv6 addresses and `tcp6`/`udp6` are
  rejected until IPv6 is wired end to end.
- gVisor is **pinned and confined to this one package**, and the
  `loom_nonetstack` build tag stubs the whole package (`New` returns
  `ErrDisabled`) so minimal agents build without the dependency:
  `go build -tags loom_nonetstack ./...`.

**Use it** only when you need TCP over an injected packet lane. And before you
attribute any TCP-derived number to the network under test, read the
measurement-hygiene numbers in the
[`core/netstack` package docs](https://github.com/bgrewell/loom/blob/main/core/netstack/doc.go):
the netstack-vs-kernel benchmark delta and the sender-side timestamp audit
(send-side stack contribution p50 ≈ 22 µs on the reference machine) are
published exactly so userspace-stack cost is quoted separately, never silently
blamed on the path.

## Choosing, in one breath

Kernel path under test → `host`. Test code → `memory`. Datagram app over a
tunnel lane → `dgram` (cheap). TCP app over a tunnel lane → `netstack` (heavy,
quantified). If you're not injecting a datapath, you want `host`.

## The RawL3 contract

The datapath-backed networks consume **layer-3 frames**: every frame payload
must be one complete IP packet, header through payload, with no link-layer
framing. A datapath declares that with one additive capability bit:

```go
type Capabilities struct {
    RawL2 bool
    RawL3 bool // frames are complete IP packets
    // …
}
```

Both `dgram.New` and `netstack.New` check it and **refuse** a backend that
doesn't advertise it — a wrong lane fails at construction with a clear error,
not at runtime with garbage packets. Note that `RawL3` is *false for every
built-in datapath*: `socket`/`memory`/`discard` carry opaque payload bytes and
AF_XDP carries L2 frames. It is true for embedder-provided raw-L3 datapaths —
the canonical example is a GTP-U inner-IP lane, where the tunnel encapsulation
owns L2/outer-IP and hands loom the inner packet.

## Registry vs direct constructors

Like every loom component, Networks resolve two ways
([ADR-0006](https://github.com/bgrewell/loom/blob/main/DECISIONS.md#adr-0006--pluggable-datapathschedulergenerator-via-a-registry)/[0022](https://github.com/bgrewell/loom/blob/main/DECISIONS.md#adr-0022--inject-component-registries-functional-options-on-constructors)):

- **The registry** (`netpath.Registry`, exposed as `Components.Networks`)
  builds from **pure-data** `netpath.Options` — a local address, an MTU, and
  *names* of Tx/Rx datapaths, never live instances — so an agent can build the
  network a `FlowSpec.network` string asks for. `host` and `memory` are always
  registered; `dgram` and `netstack` self-register when their packages are
  linked in. Stock `loomd` imports netstack — under the `loom_nonetstack` tag
  the name still resolves and fails with `ErrDisabled` instead of "unknown
  network", which is exactly what the skew gate wants to see.
- **Direct constructors** (`netpath.Host`, `netpath.Memory`, `dgram.New`,
  `netstack.New` + `Stack.Network`) take live values, for embedders that
  construct datapaths out-of-band — the per-gNB multi-address netstack shape,
  for instance, is only reachable this way.

On the wire, an app flow selects its network with two `FlowSpec` strings:
`network` (registry name, `""` = `host`) and `local` (source/bind address for
datapath-backed networks). Agents advertise their registered network names in
`CapabilitiesResponse.networks`, and consumers check them at provision time —
version skew fails fast with an actionable error instead of a mid-run surprise.

## Where to next

- **[Application engines](apps.md)** — the clients and servers that dial
  through this seam.
- **[Design: real application traffic](design/real-app-traffic.md)** — the full
  design this seam anchors.
