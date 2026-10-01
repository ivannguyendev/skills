# Client: reconnect, rejoin, resync, outbox

A reconnect is a **new socket**: no rooms, possibly an expired token, and a gap of missed messages. Every successful connect restores all three. At fleet scale the client's reconnect policy is part of server capacity.

## Reconnect policy

- Full jitter: `delay = random(0, min(cap, base × 2^attempt))`, base 1 s, cap 30–60 s. Fixed schedules make every client hit the server at the same instants.
- Honor server hints: HTTP 503 `Retry-After`, close 1012 (restart) / 1013 (try again later) → wait a random 0–N s first.
- App close codes: 4001 → refresh token, then reconnect; 4003 → stop and surface.
- Reset backoff only after ~30 s of stable connection, not on TCP open.
- Never give up silently; keep retrying at the cap and show "offline". Reconnect promptly on `online` / visibility regain (still jittered).

## Socket.IO client

```js
const socket = io(URL, {
  transports: ['websocket'],
  auth: (cb) => cb({ token: getFreshToken() }),
  reconnectionDelay: 1000,
  reconnectionDelayMax: 30_000,
  randomizationFactor: 0.5,
});

const lastSeq = new Map();
const openStreams = new Set();
const outbox = new Map();
const OUTBOX_MAX = 200;

socket.on('message:new', apply);

socket.on('connect', async () => {
  for (const id of openStreams) socket.emit('stream:join', id);
  if (!socket.recovered) await resync().catch(() => {});
  for (const id of outbox.keys()) trySend(id);
});

socket.on('disconnect', (reason) => { if (reason === 'io server disconnect') scheduleJitteredConnect(); });
socket.on('connect_error', (err) => { if (err.message === 'unauthorized') refreshToken().then(() => socket.connect()); });

function apply(msg) {
  const prev = lastSeq.get(msg.streamId) ?? 0;
  if (msg.seq <= prev) return;
  if (msg.seq > prev + 1) return resync([msg.streamId]);
  lastSeq.set(msg.streamId, msg.seq);
  render(msg);
}

async function resync(ids = [...lastSeq.keys()]) {
  const cursors = Object.fromEntries(ids.map((id) => [id, lastSeq.get(id) ?? 0]));
  const res = await socket.timeout(10_000).emitWithAck('sync', cursors);
  for (const msgs of Object.values(res)) msgs.forEach(apply);
}

export function send(streamId, body) {
  if (outbox.size >= OUTBOX_MAX) throw new Error('offline queue full');
  const clientMsgId = crypto.randomUUID();
  outbox.set(clientMsgId, { streamId, clientMsgId, body });
  trySend(clientMsgId);
}

async function trySend(id) {
  const m = outbox.get(id);
  if (!m || !socket.connected) return;
  try {
    const r = await socket.timeout(8000).emitWithAck('message:send', m);
    if (r.ok) outbox.delete(id);
  } catch {}
}
```

- `auth` as a function re-reads the token on every reconnect.
- Register listeners once, outside `connect`.
- Outbox is bounded and idempotent (`clientMsgId`); persist it (IndexedDB) if messages must survive a reload.

## Raw WebSocket client

```js
function connect(url, { onOpen, onMessage }) {
  let attempt = 0, ws, timer, stableTimer, stopped = false;
  const schedule = (minDelay = 0) => {
    const cap = Math.min(30_000, 1000 * 2 ** attempt++);
    timer = setTimeout(open, minDelay + Math.random() * cap);
  };
  const open = () => {
    clearTimeout(timer);
    ws = new WebSocket(url, ['bearer', getFreshToken()]);
    ws.onopen = () => { stableTimer = setTimeout(() => { attempt = 0; }, 30_000); onOpen(ws); };
    ws.onmessage = (e) => { try { onMessage(JSON.parse(e.data)); } catch {} };
    ws.onclose = (e) => {
      clearTimeout(stableTimer);
      if (stopped || e.code === 4003) return;
      if (e.code === 4001) return refreshToken().then(() => schedule());
      schedule(e.code === 1012 || e.code === 1013 ? 5000 : 0);
    };
  };
  open();
  return {
    send: (d) => ws.readyState === WebSocket.OPEN && ws.send(d),
    close: () => { stopped = true; clearTimeout(timer); ws.close(1000); },
  };
}
```

`onOpen` does what the Socket.IO `connect` handler does: resubscribe, resync from `lastSeq`, flush the outbox.
