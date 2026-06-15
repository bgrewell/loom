# Choosing a datapath

The **datapath** is how loom gets packets on and off the wire. Every datapath
presents the same interface, so switching is a one-word change — but they differ
enormously in speed and requirements. This guide helps you pick.

## The options

| Datapath | What it does | Needs | Use it for |
|---|---|---|---|
| `discard` | generates + accounts, drops on send | nothing | rate tests, the engine ceiling, the `loom run` default |
| `memory` | in-process loopback (zero-copy) | nothing | tests, deterministic local runs |
| `udp` | connected UDP over the kernel stack | a reachable target | portable real traffic, L4 flows |
| `tcp` | connected TCP over the kernel stack | a listening target | portable stream traffic |
| `afxdp` | AF_XDP kernel-bypass, zero-copy | a NIC (or veth) + root, an afxdp build | high packet-rate / near line-rate |

Select it on the CLI with `--datapath`, or in a scenario with `datapath:` on the
event.

## Sockets vs AF_XDP — the tradeoff

The kernel socket datapaths (`udp`/`tcp`) are the portable default: they work
anywhere, need no privileges, and are perfect up to a few Gbps per flow. Their
ceiling is the kernel — roughly **one syscall per packet**, so a single UDP
stream tops out around ~2 Gbps / ~200 K pps on a typical core.

**AF_XDP** bypasses the kernel stack. Packets live in a shared memory region
(UMEM) that the NIC fills and drains directly; loom's frames *alias* that memory,
so packet bytes are never copied, and a whole batch is submitted in one syscall.
That's how loom approaches NIC line rate. The cost is operational:

- it needs a **real NIC** (or a `veth` pair for testing) and **root**
  (`CAP_NET_RAW` + `CAP_BPF`);
- it's compiled into `loomd` with a **build tag** (`go build -tags afxdp
  ./cmd/loomd`) — a default `loomd` will reject `datapath: afxdp` with
  `InvalidArgument`;
- it operates at **layer 2**: it sends raw Ethernet frames, so over a real
  network you need frames with valid headers (fine for raw-rate tests over a
  direct link or veth today; a header-crafting generator is on the roadmap).

> **Rule of thumb.** Reach for `udp`/`tcp` first — they're simpler and fast
> enough for most tests. Move to `afxdp` when you're pushing packet rates the
> kernel can't, on hardware you control.

DPDK will join AF_XDP on the same interface later, for the highest end; the
roadmap tracks it.

## Selecting AF_XDP in a scenario

`datapath: afxdp` on the event picks the backend; each endpoint names the NIC it
uses via `iface` (and optional `queue`):

```yaml
endpoints:
  - name: tx
    iface: eth0
  - name: rx
    iface: eth1

timeline:
  - name: blast
    flow: { kind: udp, packet_size: 1000 }
    datapath: afxdp
    from: tx
    to: rx
    start: 0s
    stop: { after: 5s }
```

Run it with `loomd` built using the `afxdp` tag, as root. A complete, runnable
veth example ships at
[`docs/examples/afxdp-veth.scenario.yaml`](../examples/afxdp-veth.scenario.yaml).

## How fast is each, really?

See **[Performance](../benchmarks.md)** for measured baselines (engine ceiling,
socket, AF_XDP/veth) and how to reproduce them on your own hardware.
