# Blueprint: dynamic-webserver (random page-size HTTP matrix)

**Sources:** traffic `network/webserver/server.go`, `network/webserver/dynamic/dynamic.go`
**Target:** loom `core/generator/emul/http/` (server side)
**Status:** drafted · **action: HARVEST**

## Idea

The server side of HTTP-download synthesis: generate a random page/object of a
**requested byte size** and serve it across a matrix of HTTP versions, TLS, and
keep-alive settings — so a client emulation can pull objects of arbitrary size
over http1/2/3, secure or not, with or without keep-alive.

## Distilled core

```
GeneratePage(sizeBytes) -> random HTML with an embedded image padded to size
Serve on a port matrix:
  8081 http1     4431 https1     4432 https2     4433 https3
  5431 https1-no-keepalive   5432 https2-nka    5433 https3-nka
GetUrl(version, secure, keepalive, size) -> URL the client requests
```

## Why it's good

- Fairly complete and directly useful for the
  [https-browse emulation](emulation.md).
- Covers the realistic axes (HTTP version × TLS × keep-alive × object size) that
  matter for middlebox/CDN testing.

## Pitfalls observed

- Writes TLS cert/key to temp files **in `init()` with `log.Fatal`** — a
  surprising import side effect; move cert handling out of `init()` and return
  errors.
- One server instance per host (intended — keep that, but make it explicit).

## loom adaptation

- A webserver component the [https-browse emulation](emulation.md) drives;
  explicit cert lifecycle (not `init()`).
- Honor the scenario's `cacheable` flag via response headers (the
  [profile schema](../scenario-schema.md) already exposes it).
- One server per agent, started on demand by the controller.

## Attribution / license

traffic — © Benjamin Grewell. Relicense under loom's license.
