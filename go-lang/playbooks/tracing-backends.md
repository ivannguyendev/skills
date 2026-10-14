# Tracing backends

Where spans go after they leave the process. Checked against OTel Collector contrib 0.161.0, Jaeger 2.21.0 and Tempo 3.1.0; re-check configs when you bump image tags. SDK setup inside the service is in `grpc-production.md`.

## Contents

1. [Topology](#topology)
2. [Collector](#collector)
3. [Jaeger v2](#jaeger-v2)
4. [Tempo 3.x](#tempo-3x)
5. [Kubernetes](#kubernetes)
6. ["I see no traces"](#i-see-no-traces)

## Topology

```text
service (OTel SDK) --OTLP--> Collector (agent or gateway) --OTLP--> Jaeger | Tempo | vendor
```

Always go through a Collector: it batches and retries, does **tail sampling** (impossible in one service), redacts in one place, and switches backends without redeploying services. Tempo takes traces only, so exporting metrics straight to it logs errors every 30s.

## Collector

`assets/observability/otel-collector-config.yaml` passes `otelcol-contrib validate`; `docker compose -f assets/observability/docker-compose.yaml up -d` runs it with Jaeger on loopback.

| Piece | Why |
|---|---|
| OTLP receiver on `0.0.0.0:4317` | Receivers bind to `localhost` by default; in a container nothing can connect |
| `memory_limiter` first in every pipeline | Rejects data under pressure instead of being OOM-killed |
| `tail_sampling`: errors + slow (>500ms) + 10% of the rest | Needs the contrib image. When scaled out, put the `loadbalancing` exporter (by trace ID) in front so a trace reaches one instance |
| `batch` | Fewer, larger exports |
| `otlp_grpc/...` exporter | Current type name; `otlp` is deprecated |

```sh
docker run --rm -v "$PWD/otel-collector-config.yaml:/cfg.yaml:ro" \
  otel/opentelemetry-collector-contrib:0.161.0 validate --config=/cfg.yaml
```

## Jaeger v2

Built on the Collector, ingests OTLP natively. Jaeger v1 is EOL (2025-12-31); the Go Jaeger exporter is gone.

| Port | Purpose |
|---|---|
| 4317 / 4318 | OTLP gRPC / HTTP: the only ingest path for new code |
| 16686 | UI and query HTTP API |
| 13133 | Health |
| 6831, 6832, 14250, 14268, 9411 | Legacy; build nothing new on them |

- All-in-one keeps traces in memory. Production needs Elasticsearch/OpenSearch, Cassandra or Badger.
- Query API v3 returns OTLP JSON: `GET /api/v3/services`, `GET /api/v3/traces?query.service_name=orders&...`. Legacy `/api/services` answers 404.
- UI: search service + operation (`pkg.Service/Method`), tags `error=true`, "Min Duration" for outliers, "Compare" to diff two traces.

## Tempo 3.x

Start in monolithic (single-binary) mode: no Kafka needed. Microservices mode requires a Kafka-compatible buffer.

```yaml
stream_over_http_enabled: true
server: {http_listen_port: 3200}
distributor:
  receivers:
    otlp:
      protocols:
        grpc: {endpoint: "0.0.0.0:4317"} # localhost by default since 2.7
        http: {endpoint: "0.0.0.0:4318"}
storage:
  trace:
    backend: local # s3 | gcs | azure in production
    wal: {path: /var/tempo/wal}
    local: {path: /var/tempo/blocks}
```

New traces become searchable after 15–30s; an empty result right after sending is not data loss. TraceQL with otelgrpc attribute names:

```text
{ resource.service.name = "orders" && status = error }
{ span.rpc.method = "acme.orders.v1.OrderService/GetOrder" && duration > 500ms }
{ resource.service.name = "gateway" } >> { resource.service.name = "orders" && status = error }
```

`resource.` = process attributes, `span.` = span attributes, intrinsics (`status`, `duration`, `name`, `span:kind`) need no prefix, `>>` = has a matching descendant.

## Kubernetes

The app only needs env vars; the Collector runs as a DaemonSet (agent) or Deployment (gateway), with `k8sattributes` to add namespace/deployment/node.

```yaml
env:
  - {name: OTEL_EXPORTER_OTLP_ENDPOINT, value: "http://otel-collector.observability.svc.cluster.local:4317"}
  - {name: OTEL_EXPORTER_OTLP_INSECURE, value: "true"} # in-cluster only; TLS across trust boundaries
  - {name: OTEL_SERVICE_NAME, value: orders}
  - {name: OTEL_RESOURCE_ATTRIBUTES, value: "deployment.environment.name=prod"}
  - {name: OTEL_TRACES_SAMPLER, value: parentbased_always_on} # tail sampling decides
```

## "I see no traces"

Check in order.

| Check | Typical cause |
|---|---|
| Exporter ↔ port | `otlptracegrpc` → 4317, `otlptracehttp` → 4318, swapped |
| TLS | "authentication handshake failed": Collector is plaintext, set `OTEL_EXPORTER_OTLP_INSECURE=true` |
| Reachability (`nc -vz collector 4317`) | Receiver bound to `localhost` in the container |
| Sampling | `always_off`, ratio 0, or the parent `traceparent` said not sampled |
| Flush | `tp.Shutdown` never ran; short jobs exit before the batch exports |
| Reached the Collector? | `debug` exporter or `otelcol_receiver_accepted_spans` |
| Reached the backend? | Jaeger `/api/v3/services`; Tempo `tempo_distributor_spans_received_total` |
| Searched too early (Tempo) | Wait 30s |
| Trace split per hop | No global propagator, or a proxy/client strips `traceparent` |
