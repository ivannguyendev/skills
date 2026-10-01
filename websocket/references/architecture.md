# Architecture: tiers, fan-out selection, hot rooms, ordering, multi-region

## Tiers

| Tier | Holds | Scales by | Must not |
| --- | --- | --- | --- |
| **Connection gateway** (uWS / ws / Socket.IO) | Sockets, per-connection auth context, local subscriptions, send buffers | Connections | Run business logic, call slow services per message, own durable state |
| **Message / business services** (stateless) | Validation, authz, rate limits, dedupe, seq assignment | Requests/s | Hold sockets |
| **Store** (DB) | Messages with per-stream seq, membership | Writes/s, partitions | Sit on the fan-out path for every recipient |
| **Broker** (Redis pub/sub / sharded / Streams / Kafka) | In-flight events between tiers | Channels, throughput | Be trusted as the delivery guarantee |
| **Presence** (Redis) | Per-user connection sets with TTL | Users | Live in gateway memory |

Message path for chat (write it down for every design):

1. Client → its gateway → message service (validate, authz, dedupe on `clientMsgId`, assign `seq`).
2. Persist (unique `{streamId, seq}` and `{streamId, clientMsgId}`), ack the sender with `seq`.
3. Publish once to the stream's channel.
4. Only gateways subscribed to that channel receive it; each delivers to its local sockets.
5. Recipients that missed it (offline, dropped, slow-consumer kick) resync from the store by `seq`; offline users go to push notifications from a durable pipeline.

Small systems collapse tiers 1–2 into one process. Keep the boundaries in code anyway so they can split.

## Fan-out: how a node decides which messages it receives

**Interest-based subscription** — the default for rooms, conversations, users, documents:

- Channel per stream: `r:{roomId}`, `u:{userId}` (all of a user's tabs/devices), `doc:{id}`.
- A node subscribes to a channel when its **first** local socket needs it and unsubscribes when the **last** leaves (ref count, or uWS `app.numSubscribers(topic)`).
- Delay unsubscribe (e.g. 30 s) so reloads and reconnect storms don't churn SUBSCRIBE/UNSUBSCRIBE.
- Await the SUBSCRIBE before acking the join; resync covers any window.
- Membership changes made elsewhere (added to a group) travel as a control message on `u:{userId}` so the nodes holding that user subscribe/unsubscribe.

```js
const subscribed = new Set();
const dropTimers = new Map();

async function want(topic) {
  clearTimeout(dropTimers.get(topic));
  dropTimers.delete(topic);
  if (subscribed.has(topic)) return;
  subscribed.add(topic);
  try { await sub.subscribe(topic); } catch (e) { subscribed.delete(topic); throw e; }
}

function release(topic) {
  if (app.numSubscribers(topic) > 0 || dropTimers.has(topic) || !subscribed.has(topic)) return;
  dropTimers.set(topic, setTimeout(() => {
    dropTimers.delete(topic);
    if (app.numSubscribers(topic) === 0 && subscribed.delete(topic)) sub.unsubscribe(topic);
  }, 30_000));
}

async function join(ws, topic) {
  ws.subscribe(topic);
  ws.getUserData().topics.add(topic);
  await want(topic);
}

sub.on('message', (channel, payload) => app.publish(channel, payload, false));
```

Cost scales with **membership spread**, not node count: a 20-member room touches at most 20 nodes, usually 1–3.

**Anti-patterns**

| Pattern | Why it fails at scale |
| --- | --- |
| One global channel, every node filters locally | Every node decodes every message: O(nodes × messages) |
| Socket.IO classic `createAdapter` with many small rooms | Same: one channel per namespace, all pods receive all broadcasts |
| Classic `PUBLISH` on Redis Cluster | Propagated to every shard over the cluster bus |
| user → node registry as the routing source | Stale on crash/reconnect → silent loss; use it for presence, not routing |
| Per-socket Redis subscriptions | A reconnect storm becomes a SUBSCRIBE storm on Redis's single thread |

**Socket.IO at scale**: `createShardedAdapter(pub, sub, { subscriptionMode: 'dynamic' })` (Redis ≥ 7) — one channel per public room, only pods with members receive. `dynamic-private` adds per-socket channels (churn on every connect; prefer `user:{id}` rooms). Still broadcast to all pods: multi-room `io.to([a, b])`, `io.emit`, `fetchSockets`, `serverSideEmit`, remote `socketsJoin` — keep them off hot paths. Run `transports: ['websocket']` to remove sticky-session requirements. Classic and sharded pods cannot see each other: cut over blue/green.

## Hot and large rooms (live events, 10k–1M members)

- Fan-out is per **node**, not per user: each gateway subscribes once, uWS `app.publish` frames once for all local members.
- **Aggregate** high-rate inputs: gateways count reactions locally and flush every 100–250 ms; an aggregator broadcasts totals a few times per second.
- **Cap and sample** broadcasts (e.g. 10–20 comments/s per room, priority to host/pinned/moderated); echo a user's own action locally.
- **Latest-wins**: no per-connection queues for live state; slow viewers lose intermediate frames.
- Initial snapshot via HTTP/CDN, not the socket; join storms go through admission control.
- Isolate: per-node budget or a separate gateway pool so a mega-room cannot starve chat.

## Ordering

- Guarantee order **per stream** (conversation, document), never globally.
- One writer assigns `seq` per stream: atomic counter (`$inc`, `INCR`, DB sequence) or route all writes of a stream to one partition (Kafka key = `streamId`).
- Pub/sub preserves publish order per channel per connection, but a resync and a live message can arrive interleaved: clients apply by `seq`, drop `seq <= last`, resync on gaps.

## Multi-region

| Model | Ordering | Latency | Use when |
| --- | --- | --- | --- |
| **Home region per stream** — all writes for a stream go to its home region | Simple single-writer seq | Remote members pay one cross-region hop | Most chat/collaboration |
| **Active-active, write in sender's region** | Needs hybrid logical clock ids + deterministic tiebreak | Best for senders | Very latency-sensitive, tolerant of concurrent-order ambiguity |

- Clients connect to the nearest healthy region (GeoDNS / anycast); each region's gateways fan out locally.
- Cross-region relay through the broker/durable log: one copy per region, never per user.
- Design so a message crosses regions at most once on the delivery path.
- Size each region to absorb a failed region's users; clients reconnect with jitter and resync.
- Cells inside a region (independent gateway + broker + presence shards) bound blast radius.
