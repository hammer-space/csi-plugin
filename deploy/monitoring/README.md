# Monitoring the Hammerspace CSI driver

The driver emits OpenTelemetry **metrics** (Prometheus/OTLP) and **traces**
(console/OTLP). This directory has the pieces to scrape those metrics and view
them in Grafana:

```
deploy/monitoring/
├── victoriametrics/scrape.yml            # example scrape config (VM or Prometheus)
└── grafana/hs-csi-driver-dashboard.json  # importable "Hammerspace CSI Driver" dashboard
```

The full metric/label catalog and tracing details live in
[`docs/observability.md`](../../docs/observability.md).

## 1. Enable metrics on the driver

Metrics are **off by default**. Turn them on via environment variables on the
CSI containers (both pods run `hostNetwork: true`, so `/metrics` is served on the
Kubernetes **node host IP**):

| Variable | Controller | Node |
|---|---|---|
| `OTEL_METRICS_EXPORTER` | `prometheus` | `prometheus` |
| `OTEL_METRICS_PROMETHEUS_LISTEN` | `:9090` (default) | `:9091` (avoid clashing with the controller on a shared host) |

The node uses `:9091` because a controller pod and a node pod can land on the
same host, and both are host-networked.

Patch an existing install:

```bash
kubectl -n kube-system patch statefulset csi-provisioner -p '{"spec":{"template":{"spec":{"containers":[{"name":"hs-csi-plugin-controller","env":[{"name":"OTEL_METRICS_EXPORTER","value":"prometheus"}]}]}}}}'

kubectl -n kube-system patch daemonset csi-node -p '{"spec":{"template":{"spec":{"containers":[{"name":"hs-csi-plugin-node","env":[{"name":"OTEL_METRICS_EXPORTER","value":"prometheus"},{"name":"OTEL_METRICS_PROMETHEUS_LISTEN","value":":9091"}]}]}}}}'
```

## 2. Scrape the endpoints

Copy `victoriametrics/scrape.yml`, replace the placeholder target IPs with your
Kubernetes node IPs, and load it as your VictoriaMetrics
`-promscrape.config=/etc/victoriametrics/scrape.yml` (VM re-reads it every
`-promscrape.configCheckInterval`, no restart) or paste the two jobs under a
Prometheus `scrape_configs:` block.

Only the node running `csi-provisioner` answers on `:9090` (the rest report
`up=0`, harmless); every node answers on `:9091`.

Verify it's flowing:

```bash
curl -s 'http://<vm-host>:8428/api/v1/query?query=up{service="hs-csi"}'
curl -s 'http://<vm-host>:8428/api/v1/query?query=sum(hs_csi_anvil_requests_total)'
```

## 3. Import the Grafana dashboard

Grafana → **Dashboards → New → Import** → upload
`grafana/hs-csi-driver-dashboard.json` (uid `hs-csi-driver`), and select your
VictoriaMetrics/Prometheus datasource. Panels cover controller/node RPC latency
(p50/p95/p99), the Anvil REST client (rate/latency/status, including the
`GET /files` 404/500 type-probe traffic), and the file- and share-backed
provisioning paths.

## Traces (optional)

Set `OTEL_TRACES_EXPORTER=console` (spans to pod stdout, readable via
`kubectl logs`) or `OTEL_TRACES_EXPORTER=otlp` with
`OTEL_EXPORTER_OTLP_ENDPOINT=<collector>:4317`. See `docs/observability.md`.
