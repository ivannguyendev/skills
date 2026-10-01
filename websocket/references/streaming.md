# Streaming server → client: SSE, LLM tokens, progress, exports

SSE is plain HTTP — one long response of `id:` / `event:` / `data:` frames. It passes proxies, resumes natively, and is the default for one-way streams. `EventSource` is GET-only without custom headers; for a request body or `Authorization`, use `fetch` + a stream reader.

## Server (Express)

```js
const { once } = require('node:events');

app.post('/api/stream', express.json(), async (req, res) => {
  const ac = new AbortController();
  res.on('close', () => { if (!res.writableEnded) ac.abort(); });

  res.writeHead(200, {
    'Content-Type': 'text/event-stream; charset=utf-8',
    'Cache-Control': 'no-cache, no-transform',
    Connection: 'keep-alive',
    'X-Accel-Buffering': 'no',
  });
  res.flushHeaders();

  const send = async (event, data, id) => {
    if (res.writableEnded || res.destroyed) return;
    const frame = `${id != null ? `id: ${id}\n` : ''}event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
    if (!res.write(frame)) await once(res, 'drain', { signal: ac.signal }).catch(() => {});
  };
  const heartbeat = setInterval(() => res.write(': ping\n\n'), 15_000);

  try {
    for await (const token of await llm.stream({ prompt: req.body.prompt, signal: ac.signal })) {
      if (ac.signal.aborted) break;
      await send('token', { t: token });
    }
    await send('done', {});
  } catch {
    if (!ac.signal.aborted) await send('error', { message: 'generation_failed' });
  } finally {
    clearInterval(heartbeat);
    res.end();
  }
});
```

| Element | Why |
| --- | --- |
| `res.on('close')` not `req` | Node ≥ 16 emits `req` close once the body is read → immediate abort |
| Abort upstream on close | Closed tab stops generation and cost |
| Await `drain` when `write()` returns false | Slow client slows the producer instead of growing memory |
| `JSON.stringify` per frame | Raw newlines break SSE framing |
| `X-Accel-Buffering: no`, `no-transform`, compression off for `text/event-stream` | Proxies/gzip would deliver everything at the end |
| `: ping` every 15 s | Idle proxy timeouts (nginx default 60 s) |
| `error` event | Status 200 already sent |

nginx: `proxy_buffering off; proxy_cache off; gzip off; proxy_http_version 1.1; proxy_set_header Connection ""; proxy_read_timeout 300s;`.

## High-volume streams (exports, feeds)

- Batch many rows per event (hundreds–thousands); throttle progress events (≤ 1/s).
- `id:` = last primary key in the batch; resume with `Last-Event-ID` → query `key > lastId` ordered by key.
- Close the DB cursor in `finally`.
- If the goal is a file, prefer a streamed download (`Content-Disposition: attachment`, same cursor + backpressure) plus a small progress stream.

## Resumable job streams (long LLM agents, reports)

- Run the job detached from the request; append events to a Redis Stream / table keyed by `jobId` with increasing ids.
- The SSE endpoint replays from `Last-Event-ID`, then tails new events; `retry: 3000` sets client reconnect delay.
- Expire job events after completion + grace period.

## At scale

- SSE connections are long-lived HTTP: same fd, LB idle timeout, admission control and drain rules as WebSocket (`resilience-ops.md`).
- HTTP/1.1 browsers allow ~6 connections per origin; multiplex streams over one SSE connection or use HTTP/2.
