# Tracing backends: Collector, Jaeger v2, Grafana Tempo

Where spans go after they leave the Go process. Everything below was checked
against OpenTelemetry Collector contrib 0.161.0, Jaeger 2.21.0 and Tempo 3.1.0.
Bump the image tags deliberately and re-check the configs when you do.

## Contents

1. [Topology](#topology)
2. [OpenTelemetry Collector](#opentelemetry-collector)
3. [Jaeger v2](#jaeger-v2)
4. [Grafana Tempo 3.x](#grafana-tempo-3x)
5. [Running it in Kubernetes](#running-it-in-kubernetes)
6. [Troubleshooting: "I see no traces"](#troubleshooting-i-see-no-traces)

## Topology

```text
service (OTel SDK) --OTLP--> Collector (agent or gateway) --OTLP--> Jaeger | Tempo | vendor
```

Send OTLP to a Collector, not straight to the backend. The Collector:
- batches and retries, so the app's exporter queue stays small;
- applies **tail sampling**, which is impossible inside a single service;
- redacts attributes and drops noisy spans in one place;
- fans out to several backends, or switches backends, without redeploying
  services;
- receives metrics and logs too. Tempo accepts traces only, so an app that
  exports metrics straight to Tempo logs export errors every 30 seconds.

## OpenTelemetry Collector

`assets/observability/otel-collector-config.yaml` passes `otelcol-contrib
validate`. The parts that matter:

| Piece | Why |
|---|---|
| `receivers.otlp.protocols.grpc.endpoint: 0.0.0.0:4317` | Collector receivers bind to `localhost` by default. Inside a container that means nothing else can connect |
| `memory_limiter` first in every pipeline | Rejects data under memory pressure instead of getting OOM-killed |
| `tail_sampling`: keep errors, keep slow (>500ms), 10% of the rest | Keeps every interesting trace and cuts volume. It needs the contrib image. Every span of a trace has to reach the same instance: put the `loadbalancing` exporter (routing by trace ID) in front when you scale out |
| `batch` | Fewer, larger export requests |
| `otlp_grpc/jaeger` exporter | `otlp_grpc` is the current type name. The old name `otlp` still works but is deprecated |

Validate any change before deploying it:

```sh
docker run --rm -v "$PWD/otel-collector-config.yaml:/cfg.yaml:ro" \
  otel/opentelemetry-collector-contrib:0.161.0 validate --config=/cfg.yaml
```

## Jaeger v2

Jaeger v2 is built on the OpenTelemetry Collector and ingests OTLP natively.
Jaeger v1 reached end of life on 2025-12-31, and the Go Jaeger exporter was
removed from OTel Go long before that. Use OTLP everywhere.

| Port | Purpose |
|---|---|
| 4317 / 4318 | OTLP gRPC / HTTP (the only ingest path new code should use) |
| 16686 | UI and query HTTP API |
| 16685 | Query gRPC API |
| 5778 / 5779 | Remote sampling (HTTP / gRPC) |
| 13133 / 13132 | Health check (HTTP / gRPC) |
| 9411, 14250, 14268, 6831, 6832 | Legacy Zipkin and Jaeger protocols. Don't build anything new on them |

Local development is covered by the compose asset, or you can run the
all-in-one directly:

```sh
docker run --rm -p 16686:16686 -p 4317:4317 -p 4318:4318 jaegertracing/jaeger:2.21.0
# The image sets JAEGER_LISTEN_HOST=0.0.0.0; outside a container the default is localhost.
```

- **Storage:** the all-in-one keeps traces in memory and loses them on restart.
  For production, configure a persistent backend (Elasticsearch/OpenSearch,
  Cassandra, Badger for single-node) through the Jaeger config file.
- **Query API:** on 2.21, use the v3 API, which returns OTLP JSON:
  `GET /api/v3/services` and
  `GET /api/v3/traces?query.service_name=orders&query.start_time_min=...&query.start_time_max=...`.
  The legacy `/api/services` endpoint answered 404.
- **UI tips:**
  - Search by service plus operation (`pkg.Service/Method`).
  - Filter tags with `error=true` or `rpc.response.status_code=NOT_FOUND`.
  - Set "Min Duration" to find slow outliers.
  - The "Compare" view diffs two traces' structure, which helps with "why is
    this request slower than that one?".

## Grafana Tempo 3.x

Tempo 3.0 replaced ingesters with an architecture that separates the read and
write paths. Microservices mode now needs a Kafka-compatible buffer. Monolithic
(single-binary) mode runs every component in one process with no Kafka, and is
the right starting point:

```yaml
# tempo.yaml — single binary, local disk
stream_over_http_enabled: true
server:
  http_listen_port: 3200
distributor:
  receivers:
    otlp:
      protocols:
        grpc:
          endpoint: "0.0.0.0:4317" # defaults to localhost since Tempo 2.7
        http:
          endpoint: "0.0.0.0:4318"
storage:
  trace:
    backend: local # s3 | gcs | azure in production
    wal:
      path: /var/tempo/wal
    local:
      path: /var/tempo/blocks
usage_report:
  reporting_enabled: false
```

```sh
docker run --rm -p 3200:3200 -p 4317:4317 -v "$PWD/tempo.yaml:/etc/tempo.yaml:ro" \
  grafana/tempo:3.1.0 -config.file=/etc/tempo.yaml
```

New traces became searchable about 15–30 seconds after ingest. An empty result
right after sending doesn't mean the data was lost. Query from Grafana (Tempo
data source, `http://tempo:3200`) or the HTTP API
(`GET /api/search?q=<TraceQL>&start=<unix>&end=<unix>`).

TraceQL that works with otelgrpc's attribute names:

```text
{ resource.service.name = "orders" && status = error }
{ span:kind = server && span.rpc.response.status_code = "NOT_FOUND" }
{ span.rpc.method = "acme.orders.v1.OrderService/GetOrder" && duration > 500ms }
{ resource.service.name = "gateway" } >> { resource.service.name = "orders" && status = error }
{ name = "payments.Charge" } | count() > 3
```

- `resource.` scopes to process attributes and `span.` to span attributes.
- Intrinsics such as `status`, `duration`, `name` and `span:kind` need no
  prefix.
- `>>` means "has a descendant matching".

## Running it in Kubernetes

The app only needs environment variables. The Collector usually runs as a
DaemonSet (agent) or a Deployment (gateway):

```yaml
env:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: http://otel-collector.observability.svc.cluster.local:4317
  - name: OTEL_EXPORTER_OTLP_INSECURE
    value: "true" # in-cluster plaintext; use TLS across trust boundaries
  - name: OTEL_SERVICE_NAME
    value: orders
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: OTEL_RESOURCE_ATTRIBUTES
    value: deployment.environment.name=prod,k8s.pod.name=$(POD_NAME)
  - name: OTEL_TRACES_SAMPLER
    value: parentbased_always_on # let the Collector's tail sampling decide
```

Add the Collector's `k8sattributes` processor to enrich spans with namespace,
deployment and node, so pods don't have to report it themselves.

## Troubleshooting: "I see no traces"

Work through these in order.

| Check | How | Typical cause |
|---|---|---|
| Is the endpoint right for the exporter? | `otlptracegrpc` → port **4317**. `otlptracehttp` → **4318** | gRPC exporter pointed at 4318 (or the reverse) |
| TLS mismatch | Look in the exporter logs for "transport: authentication handshake failed" | Collector expects plaintext: set `OTEL_EXPORTER_OTLP_INSECURE=true` or an `http://` endpoint |
| Is the receiver reachable? | `nc -vz collector 4317` from the pod | Receiver bound to `localhost` inside the container; use `0.0.0.0` |
| Are spans sampled? | Look for `OTEL_TRACES_SAMPLER=always_off` or ratio `0`, and check the upstream `traceparent` flags | Parent decided "not sampled", or tail sampling dropped it |
| Did the process flush? | Make sure `Shutdown` runs on exit | Short-lived jobs or CLIs exit before the batch processor exports |
| Did spans reach the Collector? | Add the `debug` exporter, or check the Collector's `otelcol_receiver_accepted_spans` counter (Prometheus may add a `_total` suffix) | Wrong service, wrong namespace, network policy |
| Did they reach the backend? | Jaeger `/api/v3/services`. Tempo `tempo_distributor_spans_received_total` on `:3200/metrics` | Exporter misconfigured in the Collector pipeline |
| Searching too early (Tempo) | Wait 30s and search again | Data isn't searchable yet |
| Trace broken into pieces | Each hop shows a separate trace | Missing global propagator, or an uninstrumented client or proxy that strips `traceparent` |
