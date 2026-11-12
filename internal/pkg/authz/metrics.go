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

package authz

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics is the Prometheus sink for authorization decisions. A nil *Metrics
// is valid and every method is nil-safe, so authorization works unchanged when
// metrics are never wired (tests, CLI tools).
//
// It replaces the OpenTelemetry metric instruments the upstream implementation
// used: oai-agent-apiserver exposes /metrics through prometheus.DefaultGatherer and
// has no OTel metric exporter configured.
type Metrics struct {
	CheckCounter  *prometheus.CounterVec
	CheckDuration *prometheus.HistogramVec
	DenyCounter   *prometheus.CounterVec
}

// M is the process-wide metrics sink, set once by ProvideAuthzCoordinator.
var M *Metrics

// NewMetrics registers the authorization instruments on reg and returns them.
//
// A duplicate registration is tolerated rather than fatal: promauto panics on
// one, and a metrics collector colliding should never be the reason a server
// fails to start. If it does collide the returned sink is nil, which every
// method here tolerates.
func NewMetrics(reg prometheus.Registerer) (m *Metrics) {
	factory := promauto.With(reg)
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("authz: metrics already registered; authorization continues without its counters", "panic", r)
			m = nil
		}
	}()
	return &Metrics{
		CheckCounter: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "authz_check_total",
			Help: "Total number of authorization checks, by level/decision/backend.",
		}, []string{"level", "decision", "resource_kind", "verb", "backend"}),
		CheckDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "authz_check_duration_seconds",
			Help:    "Duration of a single authorization check, by level/backend.",
			Buckets: prometheus.DefBuckets,
		}, []string{"level", "backend"}),
		DenyCounter: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "authz_deny_total",
			Help: "Total number of Deny decisions, by level/resource/backend/subject.",
		}, []string{"level", "resource_kind", "verb", "backend", "subject_kind"}),
	}
}

// observeCheck records one authorization check. decision is one of
// "allow"/"deny"/"no_opinion"/"error".
func (m *Metrics) observeCheck(level, decision, resourceKind, verb, backend string, seconds float64) {
	if m == nil {
		return
	}
	m.CheckCounter.WithLabelValues(level, decision, resourceKind, verb, backend).Inc()
	m.CheckDuration.WithLabelValues(level, backend).Observe(seconds)
}

// observeDeny records one Deny decision.
func (m *Metrics) observeDeny(level, resourceKind, verb, backend, subjectKind string) {
	if m == nil {
		return
	}
	m.DenyCounter.WithLabelValues(level, resourceKind, verb, backend, subjectKind).Inc()
}
