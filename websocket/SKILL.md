---
name: websocket
description: Use when building, scaling, or operating production realtime systems — WebSocket, Socket.IO, uWebSockets.js, ws, Server-Sent Events, chat, presence, rooms, live events, notifications, LLM token streaming, multi-node fan-out over Redis/Kafka — for high connection counts, low latency, or enterprise deployment; or when seeing messages lost after reconnect, reconnect storms after deploys, p99 latency spikes, memory growing with slow clients, close code 1006, or fan-out cost growing with node count.
---

# Building WebSocket Systems (enterprise grade)

## Overview

The target is a realtime tier that holds millions of connections, delivers within a p99 budget, survives node loss and deploys, and is operable. A WebSocket is a stateful, at-most-once pipe pinned to one process: **identity, liveness, delivery, ordering, flow control, fan-out, and recovery are all things you design** — none come with the socket. The connection tier is infrastructure: keep business logic out of it.

## Workflow — each step has an exit gate

| Step | Do | Exit gate |
| --- | --- | --- |
| 1. Requirements | Fill **Scale** in the design record | Numbers written, capacity math done (`references/capacity-performance.md`) |
| 2. Architecture | Tiers, transport, library, broker, fan-out selection | The path *sender → every recipient connection* is written, including **which nodes receive each message** (`references/architecture.md`) |
| 3. Delivery | Classify every event type | Each type mapped to a guarantee and mechanism (`references/delivery.md`) |
| 4. Implement | Build with the Required pieces below | Every Trap checked (`references/security.md`, `references/client.md`, `references/streaming.md`) |
| 5. Validate | Load + chaos scenarios | All SLOs hold with ≥ 30 % headroom (`references/resilience-ops.md`) |
| 6. Operate | SLIs, alerts, drain and rollout runbook | Mass reconnect and node kill recover without an operator |

## Design record — output this before writing code

```text
Scale:      peak concurrent connections | connects/s (steady, after deploy) | inbound msg/s
            avg + max fan-out per message | payload size | p99 delivery target | availability
Transport:  WS / SSE / long-poll — why
Tiers:      connection gateway (what it holds) | message/business services | stores
Library:    uWS / ws / Socket.IO — processes per host, connections per process
Fan-out:    broker | channel scheme | how a node decides which messages it receives
Delivery:   per event type: at-most-once | at-least-once + idempotent | ordered by <key>
Flow ctl:   per-socket output limit + policy (disconnect / coalesce / drop) | inbound rate limits
Security:   auth at upgrade | token refresh on long-lived sockets | authz per room | tenant quotas
Lifecycle:  admission = connect-rate token bucket (503 + Retry-After, before auth) + per-process connection cap
            drain + rollout | client reconnect policy
SLIs/SLOs:  list + targets + alert rules
Load test:  scenarios + pass criteria
```

If the task changes one part of an existing system, fill only the rows it touches and state the assumptions for the rest. Never skip **Scale** — every later decision depends on it.

## Decision tables

### Transport

| Need | Use |
| --- | --- |
| Server → client only (feeds, notifications, LLM tokens, progress) | SSE — plain HTTP, native resume via `Last-Event-ID` |
| Bidirectional, frequent, low latency (chat, collaboration, games, trading) | WebSocket |
| Networks that block WebSocket | Long-poll fallback (costs sticky sessions) |

### Library (Node)

| Library | Fit | Cost |
| --- | --- | --- |
| uWebSockets.js | Highest connections per core, native topic pub/sub (frames once per publish), C++ TLS | Sharp edges: `req` invalid after `await`, manual backpressure handling |
| ws | Standard, flexible, pure JS | More CPU/memory per connection; you build heartbeat and fan-out |
| Socket.IO | Rooms, acks, reconnect, recovery, fallback built in | Protocol overhead; at scale use websocket-only transport + sharded adapter |

### Fan-out broker

| Broker | Guarantee | Use for |
| --- | --- | --- |
| Redis pub/sub (one channel per room/user, ref-counted subscriptions) | at-most-once | Low-latency hot path, single Redis |
| Redis sharded pub/sub (`SSUBSCRIBE`/`SPUBLISH`, Redis ≥ 7) / Socket.IO `createShardedAdapter` | at-most-once | Same on Redis Cluster; classic `PUBLISH` floods every shard |
| Redis Streams | at-least-once, replay, consumer groups | Resumable streams, moderate scale |
| Kafka / Kinesis | durable ordered log per partition | Durable pipeline (persistence, push, analytics, cross-region); gateways still fan out via pub/sub |

The hot path is at-most-once by design; **correctness comes from the durable store + seq resync**, not from the broker.

## MUST

- Size from numbers: memory per connection × connections per process, N+2 headroom, survive the largest node's reconnect burst.
- One process per core (or uWS/cluster workers); no business logic, no blocking work in the gateway event loop.
- Interest-based fan-out: a node receives a room's messages only while it has a local member.
- Serialize each broadcast payload once, not per recipient.
- Bound every per-socket buffer and every inbound rate (per user, not per socket).
- Persist, then emit, with a per-stream `seq`; clients resync from their last `seq`.
- Admission control on upgrade; paced drain with close code 1012 on deploy; jittered client reconnect.
- Liveness checks independent of dependencies; readiness gates new connections only.
- Load-test connection counts, connect bursts, slow consumers, and node kills before production.

## MUST NOT

- Store growing per-connection state in memory without a cap or a clustering strategy.
- Route every message to every node and filter locally.
- Call remote services synchronously on every connect without a cache and a circuit breaker.
- Restart a whole fleet at once, or let health checks restart nodes that are only overloaded.
- Trust the broker for delivery, or treat `connectionStateRecovery` as the guarantee.

## Required when clients send messages that others receive (chat, collaboration, commands)

Include each even if the task doesn't ask, or state why it's out of scope. For server → client only streams use just the Traps table.

- Per-user rate limit on inbound events.
- Persist, then emit, with a per-stream `seq`; unique index on `seq` and on a client `clientMsgId` (idempotent retries).
- Resync: client sends last `seq` per stream on every (re)connect and on any gap.
- Bounded output per socket with an explicit policy.
- Cleanup keyed by connection id, never by user id.

## Traps

| Trap | Fact |
| --- | --- |
| SSE cleanup on `req.on('close')` | Node ≥ 16 fires it once the body is read. Use `res.on('close')` |
| Ignoring `res.write()` return | `false` = buffer full; `await once(res, 'drain')` before producing more |
| uWS/ws `drain` as an overflow alarm | It fires when the buffer *shrinks*. Detect overflow from `send()` return (uWS 0 buffered, 2 dropped) or `bufferedAmount` |
| uWS `publish` with default `maxBackpressure` (64 KB) | Sockets over it are **silently skipped** → stale UI. Set it and `closeOnBackpressureLimit: true` |
| uWS async `upgrade` reading `req` after `await` | `req` is invalid after the first await; read headers first, `res.onAborted`, upgrade inside `res.cork` |
| `socket.volatile.emit` for state | Can drop the newest value. Only for cursors/typing |
| Socket.IO backpressure via `emit` wrappers | Socket has no `drain`. Inspect `socket.conn.writeBuffer.length` / `transport.writable` (engine.io internals — pin version) |
| `connectionStateRecovery` as the fix for loss | Not supported by the classic Redis adapter; lost on restart; time-capped |
| `disconnect` handler cleaning up by `userId` | Fires after the reconnected socket joined → wipes the new socket's state |
| Presence `HSET presence userId socketId` | Breaks with 2 tabs / 2 nodes. Per-user set of connection ids + TTL, atomic (Lua) |
| ws `maxPayload` left default | 100 MiB. Set it |
| Ack a room join before Redis `SUBSCRIBE` resolves | Messages published in that window never reach the node. Await, then resync |
| Per-recipient `JSON.stringify` in a fan-out loop | Blocks the event loop for O(members); p99 explodes. Serialize once, chunk large fan-outs |
| Session/auth service call on every connect | A deploy turns it into a thundering herd. Verify JWT locally, cache, circuit-break |

## References — load only what the task touches

| File | Load when |
| --- | --- |
| `references/architecture.md` | Tiers, multi-node fan-out and node selection, hot/large rooms, ordering, multi-region |
| `references/capacity-performance.md` | Capacity math, connections per node, latency/p99, CPU, GC, kernel and LB limits, backpressure |
| `references/delivery.md` | Seq/resync, idempotency, outbox, idempotent consumers, DLQ, effectively-once |
| `references/resilience-ops.md` | Admission control, reconnect storms, drain and rollout, health checks, SLIs/alerts, load and chaos tests |
| `references/security.md` | Upgrade auth, token lifecycle, authz, tenant quotas, abuse limits |
| `references/client.md` | Reconnect policy, resync, outbox, close-code handling |
| `references/streaming.md` | SSE, LLM token streaming, resumable job streams |
