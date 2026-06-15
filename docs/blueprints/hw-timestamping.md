# Blueprint: hw-timestamping (NIC hardware timestamps for one-way delay)

**Sources:** basicHWTimestamps `timestamp_runner.go:12-66` (consts/structs),
`:96-100` (sockopt), `:102-120` (ioctl), `:122-163` (TX error-queue retrieval);
quantify `src/UdpReceiver.cpp:48-139` (RX cmsg, C++ reference)
**Target:** loom `core/measure/owd/` + a datapath capability
**Status:** drafted · **action: HARVEST (TX) + REFERENCE (RX)**

## Idea

NIC **hardware timestamping** for accurate one-way delay — the capability beyond
software timesync. basicHWTimestamps works out the **TX** path in Go; quantify is
the **RX** reference in C++. Between them, the full request → capture → extract
sequence exists for both directions.

## Distilled core

```
enable:   ioctl(SIOCSHWTSTAMP, hwtstamp_config{tx_type, rx_filter})   // driver
          setsockopt(SOL_SOCKET, SO_TIMESTAMPING,
                     SOF_TIMESTAMPING_{TX,RX}_HARDWARE|RAW_HARDWARE)   // socket
TX stamp: recvmsg(MSG_ERRQUEUE) → ParseSocketControlMessage →
          decode scm_timestamping (3×timespec: sys, hwsys, hwraw)
RX stamp: same cmsg walk on the normal recv; recvmmsg() batching for high rate
```

The kernel constants Go's stdlib doesn't export are transcribed in
`timestamp_runner.go:12-66` — worth keeping verbatim (snippet `hwts-constants`).

## Why it's good

- The only prior art for NIC timestamps in the audit; the fiddly cmsg
  decode/error-queue dance is already solved.
- Enables true one-way delay, which gopacket/iperf can't give.

## Pitfalls observed

- Linux-only, **root**, NIC-driver-dependent; hardcoded interface.
- Uses the deprecated `syscall` package — port to `golang.org/x/sys/unix`.
- `LittleEndian` decode assumes host byte order; quantify drops `tv_sec` (bug).
- **No one-way-delay math exists anywhere** — correlating a TX stamp on host A
  with an RX stamp on host B needs a common clock (PHC/PTP); build that fresh.

## loom adaptation

- A Go `hwts` package on `x/sys/unix`, wrapped behind a clean interface and a
  **datapath capability flag** ([DESIGN §5.1](https://github.com/bgrewell/loom/blob/main/DESIGN.md#51-datapath--the-packet-io-backend-driverfirmware-layer)).
- OWD correlation layer built fresh on top of TimeSync/PHC.
- Optional — only engaged when a scenario requests one-way delay.

## Attribution / license

basicHWTimestamps — © Benjamin Grewell (relicense). quantify (C++) is reference
only; no code carried over.
