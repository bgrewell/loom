# Blueprint: traffic-engine (flow / endpoint / orchestrator model)

**Sources:** traffic `core/{server,client}.go`, `core/controllers/`,
`core/orchestrators/`, `network/endpoint/`, `configuration/types/flowConfig.go`
**Target:** loom `core/flow/`, `core/generator/`, `core/plan/`
**Status:** drafted · **action: REFERENCE (port the design)**

## Idea

The most complete generation framework in the audit. Its value is the
**architecture**, not the code: a consistent four-method lifecycle repeated across
every layer, a symmetric `trafficd` daemon where any node is client or server
per-flow, and a declarative `{Type, Params}` config that maps onto the YAML
profiles.

## Distilled core

```go
type IEndpoint     interface { Setup(); Start(); Stop(); Cleanup() }            // emits traffic
type IController   interface { Setup(clients, servers []Client); Start(); Stop(); Cleanup() }
type IOrchestrator interface { Load(); Setup(); Start(); Stop() }               // owns the timeline

type FlowConfig struct { Type string; Params map[string]interface{} }           // opaque per-protocol params
```

`Server.Setup` switches on the requested role and instantiates the right endpoint
— so one daemon serves either side of any flow. The timeline orchestrator does
tag-based endpoint selection, `oneOf`/`allOf`, client≠server exclusion, and drives
events off wall-clock offsets.

## Why it's good

- One clean lifecycle (`Setup/Start/Stop/Cleanup`) at every layer.
- Symmetric daemon = loom's [agent](../../DESIGN.md#11-roles--topology) model.
- `{Type, Params}` is exactly loom's [scenario flow-kind](../scenario-schema.md)
  shape; the YAML selection language (`tags(all(...))`, `oneOf`) ports directly.

## Pitfalls observed

- Extension is via a **`switch`** (`server.go:82`, `cmd/main.go:135`), so the
  "drop in a new protocol" promise never held — use a **registry**.
- Timeline orchestrator **busy-waits** (`time.Sleep(10µs)` loops) instead of
  timers; `log.Fatal` inside connection handlers kills the whole daemon.
- UDP/VoIP/QUIC endpoints + controllers are **stubs/no-ops**; `accounting`,
  `reporting`, `scheduler` are empty interfaces; `Cleanup`/`Stop` "not
  implemented." Only TCP + HTTP-download actually run.

## loom adaptation

- These three interfaces become loom's flow/generator/orchestration seams
  ([DESIGN §5](../../DESIGN.md#5-data-plane), [§9](../../DESIGN.md#9-orchestration-scenarios--timeline)),
  **registry-driven** not switch-driven.
- Timeline orchestrator → loom's [Timeline engine](../scenario-schema.md) with
  real timers (no busy-wait) and the seed-reproducible selection.
- Fill the holes loom owns separately: [accounting](accounting.md),
  [schedulers](schedulers.md), the non-TCP generators.

## Attribution / license

traffic — © Benjamin Grewell. Design ported; code reimplemented clean.
