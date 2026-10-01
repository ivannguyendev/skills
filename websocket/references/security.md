# Security: upgrade auth, token lifecycle, authorization, abuse limits

## Authenticate at the upgrade

Reject before the socket exists. Check `Origin` against an allowlist (browsers send cookies on cross-site WebSocket upgrades — CSWSH). Token transport, best first: cookie (same-site) or `Sec-WebSocket-Protocol: bearer, <token>` (browsers can't set headers) or Socket.IO `auth` payload; avoid query strings (they land in proxy and access logs). Pin JWT algorithms.

**Socket.IO**

```js
io.use(async (socket, next) => {
  try {
    const claims = verifyJwt(socket.handshake.auth?.token);
    socket.data.userId = String(claims.sub);
    socket.data.tenantId = claims.tid;
    socket.data.exp = claims.exp;
    next();
  } catch {
    next(new Error('unauthorized'));
  }
});
```

**ws** — `verifyClient` is discouraged; use `noServer` + HTTP `upgrade`:

```js
const wss = new WebSocketServer({ noServer: true, maxPayload: 64 * 1024 });
server.on('upgrade', (req, socket, head) => {
  socket.on('error', () => socket.destroy());
  if (!ALLOWED_ORIGINS.has(req.headers.origin)) return reject(socket, 403);
  let claims;
  try { claims = verifyJwt(tokenFrom(req)); } catch { return reject(socket, 401); }
  wss.handleUpgrade(req, socket, head, (ws) => {
    ws.ctx = { userId: String(claims.sub), tenantId: claims.tid, exp: claims.exp };
    wss.emit('connection', ws, req);
  });
});
const reject = (socket, code) => { socket.write(`HTTP/1.1 ${code} ${code === 401 ? 'Unauthorized' : 'Forbidden'}\r\nConnection: close\r\n\r\n`); socket.destroy(); };
```

**uWebSockets.js** — read everything from `req` before the first `await`:

```js
upgrade: async (res, req, context) => {
  const key = req.getHeader('sec-websocket-key');
  const protocol = req.getHeader('sec-websocket-protocol');
  const extensions = req.getHeader('sec-websocket-extensions');
  const origin = req.getHeader('origin');
  const token = tokenFromProtocol(protocol);
  let aborted = false;
  res.onAborted(() => { aborted = true; });
  const claims = ALLOWED_ORIGINS.has(origin) ? await verifyJwtAsync(token).catch(() => null) : null;
  if (aborted) return;
  res.cork(() => {
    if (!claims) return res.writeStatus('401 Unauthorized').end();
    res.upgrade({ userId: String(claims.sub), tenantId: claims.tid, exp: claims.exp, topics: new Set() },
      key, 'bearer', extensions, context);
  });
},
```

## Token lifecycle on long-lived sockets

- A socket outlives its token. Schedule a close with an app code (e.g. **4001 token expired**) at `exp`, or accept an in-band `auth:refresh` with a new token and re-verify.
- Revocation (logout, ban, permission change) pushed over pub/sub to `u:{userId}` → gateways close those sockets.
- Client on 4001: refresh, then reconnect; on 4003 (forbidden): stop.

## Authorization

- Authorize **every** subscription/join (`canJoin(userId, streamId)`), not just the connection; re-check after the await that the socket is still open.
- Authorize every inbound action against the stream; never trust ids or roles sent by the client.
- Tenancy: channel names include the tenant (`t:{tenant}:r:{room}`); a gateway never subscribes a socket to another tenant's channel.

## Abuse limits

| Limit | Where |
| --- | --- |
| Max payload | ws `maxPayload` (default 100 MiB — always set), Socket.IO `maxHttpBufferSize` (1 MB), uWS `maxPayloadLength` (16 KB) |
| Inbound rate | Token bucket per user (in-process first layer; Redis for fleet-wide); count accepted messages only |
| Connections | Per user, per tenant, per IP — IP from the address your own proxy appended, never a raw client `X-Forwarded-For` |
| Subscriptions | Max rooms per connection |
| Validation | Parse in try/catch; validate shape, types, lengths, id formats; reject binary if unused |
| Output | Escape/sanitize user content on render (XSS); never send server internals in errors |

## Transport and audit

- TLS everywhere (`wss://`); internal hops mTLS where policy requires.
- Audit log: connect/disconnect with user, tenant, IP, user agent, close code; authz denials; rate-limit hits.
