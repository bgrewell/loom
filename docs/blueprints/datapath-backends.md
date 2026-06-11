# Blueprint: datapath-backends

**Sources:** packet `internal/controller/transceiver.go:3`,
`internal/transceiver/{xdp,conn}.go`, `xdp_test.go:53-59` (gopacket recipe)
**Target:** loom `core/datapath/`
**Status:** drafted

## Idea

The packet-I/O abstraction loom's [DESIGN §5.1](../../DESIGN.md#51-datapath--the-packet-io-backend-driverfirmware-layer)
calls the **datapath**, with swappable backends. `packet` is the only audited repo
built as a proper *library* (interfaces, DI, `internal/` layout, gopacket-native)
— the right base to grow from.

## Distilled core

```go
type Transceiver interface { Connect() ; Write([]byte) ; Read([]byte) ; Close() }
```

- **AF_XDP backend** — `asavie/xdp` socket setup, auto MAC/link resolution via
  `netlink`, zero-copy descriptor TX. The hardest-to-rewrite asset in the audit.
- **Conn backend** — UDP `net.Conn`, with an atomic **PPS counter goroutine** +
  report channel (a good measurement-loop reference).
- **Layers** — eth/ip/udp via `google/gopacket` `SerializeLayers` (don't
  hand-roll headers).

## Why it's good

- Clean interface + DI matches loom's standards; gopacket-native.
- XDP/Conn backends are real; the abstraction maps onto loom's `Datapath` +
  capability set directly.

## Pitfalls observed

- `af_packet.go`, `raw.go`, `syscall.go` are **empty stubs** — AF_PACKET unbuilt.
- XDP: `Read` unimplemented, queue hardcoded to 0, dstMAC built via a wrong
  `net.HardwareAddr("ff:ff:…")` string cast (not parsed bytes).
- `RandomPayloader` is a panicking placeholder — use bperf's
  [payloaders](payloaders.md) instead.
- `Payloader` is a 6-method god interface — trim it.

## loom adaptation

- Promote `Transceiver` → loom's `Datapath` with a **capability set** (raw L2? HW
  timestamping? max pps?) so the orchestrator validates scenarios per backend.
- Keep XDP + Conn; build the **AF_PACKET** backend on `mdlayher/raw`; add a
  **`memory`/loopback** backend for deterministic tests
  ([testing Tier 1/2](../testing.md)).
- gopacket for layer encode/decode. Each backend passes the `DatapathContract`.

## Attribution / license

packet — © Benjamin Grewell. Relicense under loom's license; gopacket is BSD-3.
