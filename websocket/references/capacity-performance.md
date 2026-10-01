# Capacity and performance: sizing, latency, CPU, memory, limits

## Capacity math — do it before choosing hardware

```text
conns_per_process = min(memory_budget / mem_per_conn, cpu_budget / cpu_per_conn_at_peak)
processes         = cores (leave 1 for OS/TLS terminator if colocated)
nodes             = ceil(peak_conns × region_failover_factor / (conns_per_process × processes)) + 2
deliveries/s      = inbound_msg/s × avg_fanout   (+ typing/presence/receipts multiplier)
reconnect burst   = largest node's connections (and ~10 % of fleet) arriving within the drain window
```

- **Measure** memory per connection on your stack under realistic subscriptions and TLS; budgets of tens of KB per connection are typical — uWS is far below ws/Socket.IO.
- Keep the event loop < 50 % busy at peak; leave ≥ 30 % headroom on every resource.
- Every dependency touched on connect (auth, session, presence, SUBSCRIBE) must survive the reconnect burst at the admission-controlled rate.
- Write the result into the design record; re-check after load tests.

## Use every core

- Node is single-threaded per process. Run one worker per core:
  - uWS: several processes listening on the same port (SO_REUSEPORT on Linux).
  - Node `net`/`http`: `cluster` module, or `server.listen({ port, reusePort: true })` on Node ≥ 22.12 (Linux).
- Workers share nothing; cross-worker fan-out goes through the same broker as cross-node.
- Smaller heaps per process → shorter GC pauses.

## Latency: where p99 comes from and the fixes

| Cause | Fix | Measure |
| --- | --- | --- |
| Per-recipient serialization (`JSON.stringify({...msg, recipientId})` per socket) | Serialize once; recipient-specific data belongs in the client, or splice prebuilt Buffers | CPU profile share of stringify; event-loop delay |
| Large synchronous fan-out loop | Chunk: send to ~500–1000 sockets, `setImmediate`, continue; or uWS `publish` (native, frames once) | `perf_hooks.monitorEventLoopDelay()` p99/max |
| Slow consumers growing buffers → GC | Bounded output policy (below) | `bufferedAmount` distribution, heap/external |
| GC pauses | Fewer allocations; `--max-semi-space-size=64` (or 128) so fan-out garbage dies young; size `--max-old-space-size` to the process share | `PerformanceObserver` `gc` entries, `--trace-gc` |
| TLS in the Node event loop | Terminate TLS at an L4/L7 proxy (Envoy/HAProxy/nginx/NLB) or use uWS (C++ TLS) | Flamegraph TLS frames |
| permessage-deflate | Off for broadcast-heavy traffic (CPU + memory per socket); if needed, threshold + shared compressor | CPU, RSS per connection |
| Nagle / batching | `setNoDelay(true)` for latency-critical paths; batch small messages per tick when throughput matters more | p50 vs throughput trade |
| JSON size | Short keys, binary (msgpack/protobuf) for hot high-rate streams | Bytes/message, encode time |

Optimise one thing at a time against a reproducible load test; track delivery p50/p99/p999 measured end to end (timestamp in payload).

## Bounded output (backpressure) per library

Pick the policy per event type:

| Policy | When |
| --- | --- |
| **Disconnect** slow consumer → client reconnects and resyncs | State feeds recoverable from a snapshot or seq |
| **Coalesce** latest value per key, flush when writable | Prices, status, cursors — only the newest matters |
| **Drop** | Typing indicators, ephemeral presence pings |

**uWS** (default `maxBackpressure` 64 KB; `publish` silently skips sockets over it):

```js
app.ws('/*', {
  maxBackpressure: 256 * 1024,
  closeOnBackpressureLimit: true,
  compression: uWS.DISABLED,
  maxPayloadLength: 16 * 1024,
  idleTimeout: 32,
  sendPingsAutomatically: true,
  drain: (ws) => flushCoalesced(ws),
});
```

`send()` returns 1 sent / 0 buffered / 2 dropped. `drain` resumes coalesced sends; it never signals overflow. Native memory ≈ `maxBackpressure × slow sockets` — RSS rising while `heapUsed` stays flat means socket buffers.

**ws**:

```js
const LIMIT = 1024 * 1024;
function safeSend(ws, data) {
  if (ws.readyState !== WebSocket.OPEN) return;
  if (ws.bufferedAmount > LIMIT) return ws.terminate();
  ws.send(data);
}
```

**Socket.IO** — no per-socket drain; coalesce on a timer and skip backed-up sockets:

```js
const backedUp = (s) => !s.conn.transport.writable
  || s.conn.writeBuffer.length > 20
  || (s.conn.transport.socket?.bufferedAmount ?? 0) > 256 * 1024;

setInterval(() => {
  const now = Date.now();
  for (const socket of io.of('/').sockets.values()) {
    const d = socket.data;
    if (backedUp(socket)) {
      d.stalledSince ||= now;
      if (now - d.stalledSince > 10_000) socket.disconnect(true);
      continue;
    }
    d.stalledSince = 0;
    let batch = null;
    for (const key of d.subs) {
      const e = latest.get(key);
      if (e && d.sent.get(key) !== e.v) { (batch ||= {})[key] = e.value; d.sent.set(key, e.v); }
    }
    if (batch) socket.emit('batch', batch);
  }
}, 100);
```

`writeBuffer` / `transport.socket` are engine.io internals: pin versions. Anything that needs every event goes through seq + resync.

## Host, kernel and load balancer limits

| Limit | Set |
| --- | --- |
| File descriptors | `ulimit -n` / systemd `LimitNOFILE` ≥ 2× connections per host; `fs.nr_open`, `fs.file-max` |
| Accept queue | `net.core.somaxconn`, `net.ipv4.tcp_max_syn_backlog`; server `backlog` |
| Ephemeral ports (LB/proxy → backend) | ~28k per source IP/destination tuple by default: widen `net.ipv4.ip_local_port_range`, add backend IPs/ports or proxy source IPs |
| conntrack | Raise `nf_conntrack_max` or bypass conntrack for the WS port on gateway hosts |
| TCP memory | `net.ipv4.tcp_mem`, `tcp_rmem`, `tcp_wmem` sized for connections × buffers |
| LB idle timeout | Above heartbeat interval at every hop (LB, ingress, proxy) |
| LB algorithm | L4, least-connections, slow start for new nodes; per-LB connection quotas checked |
| Timers | No `setTimeout` per connection for heartbeats; one sweep per interval or uWS `idleTimeout` |
