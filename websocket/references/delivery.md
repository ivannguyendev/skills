# Delivery guarantees: seq, resync, idempotency, outbox, effectively-once

Socket, pub/sub and broker hot paths are at-most-once. Durable delivery = **the store as source of truth** + **a per-stream cursor clients resume from** + **idempotency at every retry boundary**.

## Classify every event type

| Class | Examples | Guarantee | Mechanism |
| --- | --- | --- | --- |
| Durable | Messages, edits, orders, document ops | Every recipient eventually sees it, in order | Persist-then-emit + seq + resync |
| Latest-state | Prices, status, presence, counters | Recipient converges to newest value | Coalesce; snapshot on (re)connect |
| Ephemeral | Typing, cursors, reactions bursts | Best effort | Drop under pressure, never replay |

## Persist, then emit

```js
socket.on('message:send', async ({ streamId, clientMsgId, body }, ack) => {
  if (!allow(socket.data.userId)) return ack({ ok: false, error: 'rate_limited' });
  try {
    const { seq } = await Counter.findOneAndUpdate(
      { _id: streamId }, { $inc: { seq: 1 } }, { upsert: true, new: true });
    const msg = await Message.create({ streamId, seq, clientMsgId, from: socket.data.userId, body });
    await publish(`r:${streamId}`, encode(msg));
    ack({ ok: true, seq });
  } catch (e) {
    if (e.code === 11000) {
      const msg = await Message.findOne({ streamId, clientMsgId }).lean();
      return ack({ ok: true, seq: msg.seq });
    }
    ack({ ok: false, error: 'internal' });
  }
});
```

- Unique indexes `{streamId, seq}` and `{streamId, clientMsgId}`; a retried send returns the original seq.
- Emit only after commit. A crash between commit and publish is healed by resync.
- A gap in seq numbers from a failed insert is harmless if clients resync on gaps and the server returns what exists.

## Resync

- Client keeps `lastSeq` per stream; sends `{streamId: lastSeq}` on every (re)connect without recovered state and on any gap (`seq > last + 1`).
- Server authorizes each stream, returns `seq > lastSeq` sorted, paged (`more: true`).
- Client applies by seq: drop `<= last`, append `== last + 1`, resync on gap. Live and resync messages may interleave — that is fine under these rules.
- **Resume token** (optional): server-issued `{connEpoch, cursors}` lets a reconnect skip full rehydration.

Socket.IO `connectionStateRecovery` restores id, rooms, `socket.data` and missed packets when `socket.recovered` is true — supported by in-memory, Redis Streams and MongoDB adapters, not the classic Redis adapter; time-capped; lost on restart. Treat it as a fast path; resync stays.

SSE equivalent: `id: <seq>` per event, replay from `Last-Event-ID` (see `streaming.md`).

## Database + broker (transactional outbox)

When a state change must also produce an event for other services, writing DB and broker separately is a dual-write: one can succeed alone.

1. In the same transaction as the business write, insert an `outbox` row `{id, aggregateId, type, payload, createdAt}`.
2. A relay (poller with `SKIP LOCKED`, or a change stream / CDC) publishes rows in order per aggregate and marks them sent.
3. Delivery to the broker is at-least-once → every consumer must be idempotent.

## Idempotent consumers, retries, DLQ

```js
async function handle(event) {
  const done = await redis.set(`done:${event.id}`, 1, 'NX', 'EX', 7 * 86_400);
  if (!done) return;
  try {
    await process(event);
  } catch (e) {
    await redis.del(`done:${event.id}`);
    throw e;
  }
}
```

- Prefer a DB unique constraint on `event.id` inside the same transaction as the side effect when the side effect is a DB write — that is atomic, the Redis key is not.
- Retry with exponential backoff + jitter, bounded attempts.
- After N failures route to a dead-letter queue with the error, alert, and provide a replay tool. Never retry forever, never drop silently.

## Effectively-once

Exactly-once end to end does not exist across a network; build **effectively-once**: at-least-once delivery + idempotent processing keyed by a stable event id + ordered application by seq. Kafka transactions give exactly-once only inside Kafka read-process-write; side effects outside still need idempotency.
