# Observability: metrics & tracing

This document describes the observability subsystem of the Hammerspace CSI
driver — the OpenTelemetry (OTel) metrics and traces it emits, how they are
wired, what is instrumented, and how to extend it. It is the design reference
for a body of work that reconstructed and extended the driver's instrumentation;
if you are changing metrics, adding a Grafana panel, or wondering why a code path
is wrapped the way it is, start here.

> Scope note: this covers the **observability** functionality specifically.
> Behavioral bug fixes and performance improvements (e.g. the share-type-probe
> optimization, the DeleteSnapshot routing fix) are documented inline at the call
> site and in their commit messages, not here.

## 1. Design goals

- **Zero cost when off.** With no exporter configured the OTel global providers
  are no-ops, so every instrument call compiles to a cheap no-op. The driver
  must be shippable with instrumentation always compiled in.
- **Env-driven, no code changes to toggle.** Operators turn telemetry on/off and
  point it at a backend purely through standard OTel environment variables.
- **Answer "where did the time go?"** for both provisioning shapes. Metrics must
  localize latency to a specific internal step, symmetrically for **file-backed**
  and **share-backed** volumes.
- **Bounded cardinality.** No metric label may carry a per-volume identifier.

## 2. Wiring

`initTelemetry()` (`main.go`) installs the OTel providers from standard env vars
and is a no-op unless an exporter is selected:

| Env var | Values | Default | Effect |
|---|---|---|---|
| `OTEL_TRACES_EXPORTER` | `none` \| `console` \| `otlp` | `none` | trace exporter |
| `OTEL_METRICS_EXPORTER` | `none` \| `prometheus` \| `otlp` | `none` | metric exporter |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `host:4317` | — | OTLP target (traces/metrics) |
| `OTEL_METRICS_PROMETHEUS_LISTEN` | `:9090` | `:9090` | Prometheus scrape listen addr |

When `OTEL_METRICS_EXPORTER=prometheus`, the driver serves `/metrics` (and
`/healthz`) on `OTEL_METRICS_PROMETHEUS_LISTEN`. In our deployment the controller
exposes `:9090` and the node DaemonSet `:9091`, and VictoriaMetrics scrapes both.

## 3. Metric catalog

All instruments live in `pkg/common/metrics.go`. OTel → Prometheus name mapping
turns attribute keys `foo.bar` into label `foo_bar`; a counter whose instrument
name already ends in `_total` is **not** double-suffixed.

### `hs_csi_operation_*` — the per-operation family

Emitted by `MeasureOp` (see §4). Every instrumented operation/step reports:

| Series | Type | Meaning |
|---|---|---|
| `hs_csi_operation_duration_seconds` (`_bucket`/`_count`/`_sum`) | histogram | step latency |
| `hs_csi_operation_errors_total` | counter | steps that returned a non-nil error |
| `hs_csi_operation_inflight` | up/down gauge | concurrent executions of the step |

Labels: `operation` (always) plus any per-site attributes (`http_method`,
`http_path`, `fsType`).

Histogram buckets are **explicit and sub-second-biased** —
`0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30` (seconds) —
because the OTel defaults (`0, 5, 10, 25, …`) dump every sub-5s CSI call into one
bucket, making `histogram_quantile` useless.

### `hs_csi_anvil_requests_total` — the Anvil REST counter

A dedicated counter incremented once per `HammerspaceClient.doRequest`, **after**
the response is known, so it can carry the real status code.

Labels: `http_method`, `http_route`, `http_status_code`.

Why separate from the `doRequest` histogram? `MeasureOp` arms its recording in a
deferred closure set up *before* the request runs, so it can only label
method+path — it cannot know the status code. This counter fills that gap: it is
the only place we can see, e.g., the volume of `404` responses (the file-vs-share
type-probe traffic) split by route and method.

## 4. The `MeasureOp` contract

```go
func MeasureOp(ctx, operation string, attrs ...attribute.KeyValue) func(*error)
```

Call it at the start of a step; it bumps `inflight`, and returns a closure that —
when invoked (typically via `defer`) — records duration, decrements `inflight`,
and (if handed a non-nil `*error`) increments `errors_total`:

```go
func (d *CSIDriver) CreateVolume(...) (_ *csi.CreateVolumeResponse, err error) {
    defer common.MeasureOp(ctx, "Controller/CreateVolume")(&err)   // error-aware
    ...
}

defer common.MeasureOp(ctx, "FormatDevice", attribute.String("fsType", fsType))(nil) // no error tracking
```

Pass `(&err)` (with a named return) to feed `errors_total`; pass `(nil)` when the
step's error is already surfaced elsewhere (e.g. client calls whose failures show
up as non-2xx in `hs_csi_anvil_requests_total`).

## 5. Anvil route normalization (`AnvilRoute`)

`doRequest` labels carry a **route template**, not the raw path, so the metric
does not explode into one series per share/file/task. `AnvilRoute` collapses
per-resource id segments to `{id}`:

```
/mgmt/v1.2/rest/shares/file--pvc-<uuid>   ->  /mgmt/v1.2/rest/shares/{id}
/mgmt/v1.2/rest/tasks/<id>                ->  /mgmt/v1.2/rest/tasks/{id}
/mgmt/v1.2/rest/file-snapshots/list       ->  (kept; "list" is an action, not an id)
```

Collections normalized: `shares`, `tasks`, `files`, `objectives`, `file-snapshots`
(except the `list` action). The same template is used for the histogram's
`http_path` label and the counter's `http_route` label.

## 6. Instrumentation coverage

The top-level CSI RPCs and the file-backed steps were instrumented first; the
share-backed steps were added to reach **symmetry** — so latency can be localized
regardless of volume shape.

| Layer | Operations |
|---|---|
| Controller RPCs | `Controller/CreateVolume`, `Controller/DeleteVolume`, `Controller/CreateSnapshot` |
| Node RPCs | `Node/NodeStageVolume`, `Node/NodeUnstageVolume`, `Node/NodePublishVolume`, `Node/NodeUnpublishVolume` |
| File-backed steps | `MakeEmptyRawFile`, `FormatDevice`, `MountShare`, `UnmountFilesystem`, `UnmountBackingShareIfUnused` |
| **Share-backed steps** | `HammerspaceClient.WaitForTaskCompletion`, `HammerspaceClient.CreateShare`, `HammerspaceClient.CreateShareFromSnapshot`, `SetMetadataTags`, `ensureShareBackedVolumeExists`, `ensureBackingShareExists` |
| REST | `HammerspaceClient.doRequest` (histogram) + `hs_csi_anvil_requests_total` (counter) |

**`WaitForTaskCompletion` is the key share-path signal.** `CreateShare` returns
`202 + a task`; the driver then polls `GET /tasks/{id}` until terminal. That poll
is the dominant cost of share creation — the share-backed analog of the
file-visibility poll on the file path — so it gets both a metric and a span that
records the **attempt count**.

## 7. Tracing

The same call sites that carry a metric generally also open a span
(`tracer.Start`). Two spans record loop **attempt counts** as span attributes,
because attempts (not just wall time) reveal backend convergence latency:

- `applyObjectiveAndMetadata.waitForFileVisible` (file path)
- `HammerspaceClient.WaitForTaskCompletion` (share path)

Traces export via `OTEL_TRACES_EXPORTER` (`otlp` to a collector, or `console` for
local debugging).

## 8. Grafana dashboard

Dashboard **"Hammerspace CSI Driver"** (uid `hs-csi-driver`) is backed by the
VictoriaMetrics datasource. Relevant rows:

- **CSI Controller / Node RPC latency** — `histogram_quantile` p50/p95/p99 over
  `hs_csi_operation_duration_seconds_bucket` by `operation`.
- **Anvil REST client** — call rate/latency/count for `operation="HammerspaceClient.doRequest"`.
- **Share-backed provisioning path** — per-step latency/rate/inflight for the
  share operations, plus a dedicated `WaitForTaskCompletion` p50/p95/p99 panel.
- **Anvil REST calls — method / route / status** — `hs_csi_anvil_requests_total`
  by `http_status_code`, `http_route`, `http_method`, including a "404 type-probe
  rate by route" panel.

Representative queries:

```promql
# per-operation p95 latency
histogram_quantile(0.95, sum by (le, operation)(
  rate(hs_csi_operation_duration_seconds_bucket[5m])))

# Anvil calls/sec by status code
sum by (http_status_code)(rate(hs_csi_anvil_requests_total[5m]))

# 404 type-probe rate by route
sum by (http_route)(rate(hs_csi_anvil_requests_total{http_status_code="404"}[5m]))
```

## 9. Cardinality discipline

- Never put a per-volume id in a label. Anvil paths are templated via
  `AnvilRoute`; volume ids appear only in **trace** attributes, never metrics.
- `http_status_code` is bounded (a handful of codes); `operation` and `route` are
  bounded sets.
- Prefer adding an `operation` value over adding a new label.

## 10. Extending it

**Add a measured step:**
```go
func (d *CSIDriver) doThing(ctx context.Context, ...) (err error) {
    ctx, span := tracer.Start(ctx, "doThing")
    defer span.End()
    defer common.MeasureOp(ctx, "doThing")(&err) // or (nil)
    ...
}
```
The new `operation` value flows into the existing per-operation panels
automatically; add a focused panel only if the step deserves its own callout.

**Add a dashboard panel:** query `hs_csi_operation_*` filtered by `operation`, or
`hs_csi_anvil_requests_total` by its labels. Keep `rate()` windows at `[5m]` for
the bursty provisioning workload.

## 11. Rationale (why it looks like this)

- **Metrics mirror spans.** `MeasureOp` was designed to sit alongside the existing
  tracing spans so a step is measured and traced from the same call site.
- **Explicit buckets.** CSI operations are sub-second to low-seconds; default OTel
  buckets give no quantile resolution there.
- **A separate Anvil counter.** Status codes are only known post-response;
  `MeasureOp`'s pre-armed closure cannot capture them. The counter also makes
  non-GET verbs and error responses first-class.
- **Route templating over raw paths.** One series per share (`file--pvc-<uuid>`)
  is unusable and unbounded; the route template keeps the series count flat.
