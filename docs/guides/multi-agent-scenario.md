# Run a multi-agent scenario

A single `loom run` is one flow on one host. A **scenario** runs many flows
between many endpoints on a timeline, driven across a fleet of **agents**. This
guide runs one end to end — on a single machine first (two agents on different
ports), which is exactly how a real multi-host run works, just with localhost
addresses.

You'll use two binaries beyond the CLI:

- **`loomd`** — the agent; runs on every host that sends or receives traffic.
- **`loomctl`** — the controller; reads the scenario and drives the agents.

```console
go install github.com/bgrewell/loom/cmd/loomd@latest
go install github.com/bgrewell/loom/cmd/loomctl@latest
```

## 1. Start two agents

`loomd` defaults to `127.0.0.1:9551` (loopback — safe by default). Run two on one
host by giving them different addresses:

```console
# terminal 1
LOOMD_ADDR=127.0.0.1:9551 loomd

# terminal 2
LOOMD_ADDR=127.0.0.1:9552 loomd
```

Each prints `loomd control plane listening on 127.0.0.1:955x`. On separate
machines you'd instead set `LOOMD_ADDR=:9551` to bind a routable interface (and
set a token — see [Securing the control plane](securing-the-control-plane.md)).

## 2. Write a scenario

A scenario names the **endpoints** traffic runs between and a **timeline** of
what runs when. Save this as `home.scenario.yaml`:

```yaml
scenario: hello-fabric
description: one UDP flow, client -> server
seed: 1

endpoints:
  - name: client
    address: 127.0.0.1
  - name: server
    address: 127.0.0.1

timeline:
  - name: blast
    flow:
      kind: udp
      packet_size: 1200
      rate: 50Mbps
    from: client
    to: server
    start: 0s
    stop:
      after: 10s
```

- **endpoints** are logical names; `loomctl` maps each to an agent address (next
  step). `address` is the data-plane host the sender targets; omit it and loom
  uses the agent's control host.
- the **event** `blast` runs a `udp` flow from `client` to `server`, paced to
  50 Mbps, for 10 s, starting immediately.
- **seed** makes any randomness reproducible.

The full grammar — tag selectors, `oneOf`/`allOf`, repeats with randomized
intervals, all the stop conditions — is in the
[scenario schema](../scenario-schema.md).

## 3. Drive it

Map each endpoint name to an agent address and run:

```console
loomctl run \
  -f home.scenario.yaml \
  -a 'client=127.0.0.1:9551,server=127.0.0.1:9552' \
  --horizon 12s
```

`loomctl` time-syncs the agents, places a **receiver** on `server` and a
**sender** on `client`, starts both, and streams a live aggregate while the run
proceeds:

```
time-sync client: offset 12µs, delay 110µs
time-sync server: offset 9µs, delay 98µs
running scenario "hello-fabric" across 2 agents
[14:03:01] tx 49.9 Mbps  rx 49.9 Mbps  (2 flows)
...
done: placed 2 flows
```

- `--horizon` bounds how long the controller drives the timeline (give it a hair
  more than your longest event).
- `--live` (default) streams the aggregate; `--output json` emits it as JSON for
  dashboards or capture; `--interval` sets the cadence.

## Going multi-host

Nothing changes except the addresses:

1. install and run `loomd` on each host (bind a routable address: `LOOMD_ADDR=:9551`),
2. point `-a` at those hosts (`client=10.0.0.11:9551,server=10.0.0.12:9551`),
3. set a shared token so the agents aren't open — see
   [Securing the control plane](securing-the-control-plane.md).

The data plane flows agent-to-agent; `loomctl` only coordinates, so it can run
from your laptop.

## Next

- **[Choosing a datapath](datapaths.md)** — swap `udp` for `afxdp` to push real
  rates (add `datapath: afxdp` to the event and `iface:` to the endpoints).
- **[Scenario schema](../scenario-schema.md)** — selectors, repeats, and the full
  value grammar.
