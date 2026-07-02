/*
Copyright 2019 Hammerspace

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package common

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Operation metrics. These are the instruments the Grafana "Hammerspace CSI
// Driver" dashboard queries:
//
//	hs_csi_operation_duration_seconds  (histogram) - per-operation latency
//	hs_csi_operation_errors_total      (counter)   - per-operation error count
//	hs_csi_operation_inflight          (up/down)   - concurrent operations
//
// all keyed by an `operation` label plus any extra attributes the call site
// supplies (e.g. http.method, http.path, fsType -> http_method/http_path/fsType
// after the OTel->Prometheus name mapping).
var (
	opMetricsOnce sync.Once
	opDuration    metric.Float64Histogram
	opErrors      metric.Int64Counter
	opInflight    metric.Int64UpDownCounter
)

// initOpMetrics binds the package instruments to the currently-installed global
// MeterProvider. It runs once, lazily, on the first MeasureOp call - which
// happens while serving an RPC, i.e. well after main.init() has installed the
// real (Prometheus) MeterProvider. With no exporter configured the global meter
// is a no-op and every instrument call is free.
func initOpMetrics() {
	m := otel.Meter("github.com/hammer-space/csi-plugin")
	opDuration, _ = m.Float64Histogram(
		"hs_csi_operation_duration_seconds",
		metric.WithDescription("Duration of a CSI operation or internal step, in seconds"),
		metric.WithUnit("s"),
		// Sub-second-friendly buckets: the OTel default boundaries (0,5,10,25,...)
		// are far too coarse for CSI ops, dumping every sub-5s call into one bucket
		// so histogram_quantile just returns the bucket midpoint. These give real
		// p50/p95/p99 resolution from milliseconds up to 30s.
		metric.WithExplicitBucketBoundaries(
			0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30,
		),
	)
	opErrors, _ = m.Int64Counter(
		"hs_csi_operation_errors_total",
		metric.WithDescription("Number of CSI operations/steps that returned an error"),
	)
	opInflight, _ = m.Int64UpDownCounter(
		"hs_csi_operation_inflight",
		metric.WithDescription("Number of in-flight CSI operations/steps"),
	)
}

// MeasureOp instruments a CSI operation or internal step, mirroring the tracing
// spans. Call it at the start of the operation; it bumps the in-flight gauge and
// returns a closure that - when invoked, typically via defer - records the
// elapsed duration, decrements the in-flight gauge, and (when passed a non-nil
// error) increments the error counter:
//
//	defer common.MeasureOp(ctx, "Controller/CreateVolume")(nil)
//	defer common.MeasureOp(ctx, "FormatDevice", attribute.String("fsType", fsType))(nil)
//
// Metrics are labeled by `operation` plus any supplied attributes.
func MeasureOp(ctx context.Context, operation string, attrs ...attribute.KeyValue) func(*error) {
	opMetricsOnce.Do(initOpMetrics)
	start := time.Now()
	labels := make([]attribute.KeyValue, 0, len(attrs)+1)
	labels = append(labels, attribute.String("operation", operation))
	labels = append(labels, attrs...)
	opt := metric.WithAttributes(labels...)
	opInflight.Add(ctx, 1, opt)
	return func(errp *error) {
		opInflight.Add(ctx, -1, opt)
		opDuration.Record(ctx, time.Since(start).Seconds(), opt)
		if errp != nil && *errp != nil {
			opErrors.Add(ctx, 1, opt)
		}
	}
}

// Anvil REST request counter. Every call to HammerspaceClient.doRequest records
// one increment here, labeled by HTTP method, a low-cardinality route template
// (per-resource IDs collapsed to {id}), and the response status code (0 on a
// transport error). Unlike the doRequest latency histogram - which is recorded
// from a deferred closure set up *before* the response is known and therefore
// can only carry method+path - this is recorded *after* the response, so it
// carries the real status code. Together they let the dashboard show every Anvil
// call (GET/POST/PUT/DELETE) split by outcome, including the 404 type-probes that
// dominate file-backed traffic.
var (
	anvilReqOnce sync.Once
	anvilReqs    metric.Int64Counter
)

func initAnvilReqMetric() {
	m := otel.Meter("github.com/hammer-space/csi-plugin")
	anvilReqs, _ = m.Int64Counter(
		"hs_csi_anvil_requests_total",
		metric.WithDescription("Total Anvil REST requests, by HTTP method, route template, and status code"),
	)
}

// RecordAnvilRequest counts a single Anvil REST call. Call it exactly once per
// request, after the response (or transport error, in which case statusCode is
// 0) is known. Labels map to Prometheus as http_method, http_route,
// http_status_code.
func RecordAnvilRequest(ctx context.Context, method, route string, statusCode int) {
	anvilReqOnce.Do(initAnvilReqMetric)
	anvilReqs.Add(ctx, 1, metric.WithAttributes(
		attribute.String("http.method", method),
		attribute.String("http.route", route),
		attribute.Int("http.status_code", statusCode),
	))
}

// AnvilRoute collapses per-resource identifiers in an Anvil REST URL path down to
// a stable, low-cardinality template, so metrics don't explode into one series
// per share/file/task/snapshot. e.g. /mgmt/v1.2/rest/shares/file--pvc-<uuid>
// becomes /mgmt/v1.2/rest/shares/{id}. Collection "action" segments such as
// file-snapshots/list are preserved. Query strings never reach here (callers pass
// req.URL.Path), so /files?path=... is already just /files.
func AnvilRoute(urlPath string) string {
	segs := strings.Split(urlPath, "/")
	for i := 1; i < len(segs); i++ {
		switch segs[i-1] {
		case "shares", "tasks", "files", "objectives":
			if segs[i] != "" {
				segs[i] = "{id}"
			}
		case "file-snapshots":
			if segs[i] != "" && segs[i] != "list" {
				segs[i] = "{id}"
			}
		}
	}
	return strings.Join(segs, "/")
}
