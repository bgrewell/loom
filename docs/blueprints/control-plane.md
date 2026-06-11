# Blueprint: control-plane (agent lifecycle + negotiation handshake)

**Sources:** anapp `interface/proto/control.proto` (lifecycle + enums);
bperf `control/{message_setup,message_flow}.go`, `client/tcp_client.go:36-81`,
`server/tcp_server.go:47-103` (handshake)
**Target:** loom `control/`, `proto/`
**Status:** drafted · **action: REFERENCE (port the design)**

## Idea

The dedicated control channel between controller and agents
([DESIGN §8](../../DESIGN.md#8-control-plane--security)). anapp gives the
**proto/state-machine shape**; bperf gives a **concrete negotiation handshake**
with ephemeral data-port assignment.

## Distilled core

```protobuf
// anapp: endpoint lifecycle
rpc Setup; rpc Start; rpc Stop; rpc Destroy; rpc PollStatus; rpc PollAll;
enum Protocol     { TCP; UDP; ICMP }
enum TrafficDir   { TO_SERVER; TO_CLIENT; BOTH }
enum TrafficState { READY; RUNNING; FINISHED }
```

```
// bperf: handshake (the useful bit)
client → SetupMessage(flow spec)
server → net.Listen(":0"); assign ephemeral port back into the flow
server → ServerReady(port)
client → ClientReady → traffic starts
```

## Why it's good

- A well-shaped lifecycle API + state enums that map onto loom's agents.
- The **ephemeral-port negotiation** (`net.Listen(":0")` → return the port) is the
  pattern loom needs to allocate data-plane ports per flow — and it's native, so
  it survives dropping the iperf-based version.

## Pitfalls observed

- anapp `Destroy`/`PollStatus`/`PollAll` are `Unimplemented`, and the status proto
  messages are **empty** — so there's **no telemetry readback** path. loom must
  add that.
- Singleton control server via `sync.Once` (awkward for a library); single-client
  (overwrites `Client` on each accept); `log.Fatal` on a transient read.
- bperf's data plane *behind* the handshake is vaporware (schedulers/transmit are
  stubs) — take the handshake, not the transport.

## loom adaptation

- loom's gRPC control service ([DESIGN §8](../../DESIGN.md#8-control-plane--security)):
  `Register / Capabilities / Configure / Arm / Start / Stop / Destroy /
  StreamTelemetry / Health / TimeSync` — i.e. anapp's lifecycle **plus** the
  telemetry readback it lacked.
- Map `Protocol`/`TrafficDir`/`TrafficState`; reuse the ephemeral-port handshake.
- No singleton; multi-flow; optional auth token + mTLS
  ([ADR-0014](../../DECISIONS.md#adr-0014--simple-auth-not-rbac)).

## Attribution / license

anapp (proto design), bperf (handshake) — © Benjamin Grewell. Reimplemented clean.
