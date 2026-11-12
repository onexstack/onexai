// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mirror

import (
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// The projection is only as trustworthy as its lag, so that is the one metric to
// alert on: a lag that keeps growing means the mirror is falling behind the
// control plane and every List served from it is stale.
var (
	lagSeconds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "onexai_mirror_lag_seconds",
		Help: "Seconds since the projection of this kind was last verified against the control plane.",
	}, []string{"kind"})

	rows = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "onexai_mirror_rows",
		Help: "Rows currently projected for this kind.",
	}, []string{"kind"})

	writesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "onexai_mirror_writes_total",
		Help: "Rows written to the projection, by kind and operation.",
	}, []string{"kind", "op"})

	writeErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "onexai_mirror_write_errors_total",
		Help: "Batches that failed to reach the projection.",
	})

	sweepDeletedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "onexai_mirror_sweep_deleted_total",
		Help: "Rows removed by the sweep because the control plane no longer has them.",
	}, []string{"kind"})

	sweepSkippedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "onexai_mirror_sweep_skipped_total",
		Help: "Sweeps that declined to delete rows, by kind and reason.",
	}, []string{"kind", "reason"})
)

func init() {
	for _, collector := range []prometheus.Collector{
		lagSeconds, rows, writesTotal, writeErrorsTotal, sweepDeletedTotal, sweepSkippedTotal,
	} {
		register(collector)
	}
}

// register tolerates a duplicate registration: a metric colliding must never be
// the reason a controller manager fails to start.
func register(collector prometheus.Collector) {
	defer func() { _ = recover() }()
	crmetrics.Registry.MustRegister(collector)
}
