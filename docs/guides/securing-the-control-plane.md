# Securing the control plane

A loom **agent** (`loomd`) executes whatever flows it's told to — it's a
remotely-aimable traffic generator. So who can reach it, and whether they're
authenticated, matters. This guide covers the controls and the safe defaults.

## The threat in one sentence

An unauthenticated, network-reachable `loomd` lets anyone who can connect aim
traffic at a target of their choosing. loom's defaults prevent that; exposing an
agent on a network is an explicit, authenticated opt-in.

## Default: loopback-only

Out of the box `loomd` binds **`127.0.0.1:9551`** — reachable only from the same
host. Nothing off-box can talk to it. For local testing you need nothing more.

## Exposing an agent (the opt-in)

To drive an agent from another machine, bind a routable address with
`LOOMD_ADDR` **and** set a shared token with `LOOMD_TOKEN`:

```console
LOOMD_ADDR=:9551 LOOMD_TOKEN="$(cat /etc/loom/token)" loomd
```

The token is a shared secret presented as a bearer credential on **every** RPC
and checked in constant time; without a matching token, calls are rejected with
`Unauthenticated`. Bind a routable address **without** a token and `loomd` starts
but prints a loud warning — that configuration is for trusted networks only.

## Giving the controller the token

`loomctl` presents the same token, from the `--token`/`-t` flag or the
`LOOM_TOKEN` environment variable:

```console
export LOOM_TOKEN="$(cat /etc/loom/token)"
loomctl run -f scenario.yaml -a 'client=10.0.0.11:9551,server=10.0.0.12:9551'
```

The token guards the control plane (configure/start/stop and the telemetry
stream). The data plane is agent-to-agent traffic and isn't part of this exchange.

## Built-in resource limits

Even authenticated, an agent protects itself from a buggy or hostile controller:

- **packet size is bounded** (rejected outside a sane range) so one request can't
  trigger a multi-gigabyte allocation;
- **concurrent flows are capped** (configurable) so a flood of `Configure` calls
  can't exhaust file descriptors, ports, or memory;
- a flow that panics is contained to that flow — it never takes down the agent.

## What's coming

The current model is the "simple shared token" half of the security design
([ADR-0014](https://github.com/bgrewell/loom/blob/main/DECISIONS.md)). Still to come: **mTLS** for transport encryption
and per-agent identity, and a **fleet enrollment** flow for standing up many
agents across a datacenter. Until those land, run exposed agents on trusted
management networks and treat the token as a secret (distribute it with your
config-management tooling, not in scenario files).

See [Deployment](../deployment.md) for running agents as services.
