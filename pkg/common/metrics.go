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
func MeasureOp(ctx context.Context, operation string, attrs ...attribute.KeyValue) func(error) {
	opMetricsOnce.Do(initOpMetrics)
	start := time.Now()
	labels := make([]attribute.KeyValue, 0, len(attrs)+1)
	labels = append(labels, attribute.String("operation", operation))
	labels = append(labels, attrs...)
	opt := metric.WithAttributes(labels...)
	opInflight.Add(ctx, 1, opt)
	return func(err error) {
		opInflight.Add(ctx, -1, opt)
		opDuration.Record(ctx, time.Since(start).Seconds(), opt)
		if err != nil {
			opErrors.Add(ctx, 1, opt)
		}
	}
}
