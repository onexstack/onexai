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
// The original repo for this file is https://github.com/onexstack/onex.

// Package metrics exposes the agent service's Prometheus instruments.
//
// They are Prometheus rather than OpenTelemetry, unlike the implementation this
// service was ported from. /metrics is served from prometheus.DefaultGatherer,
// and these instruments register there directly: the OTel exporter onexstack
// configures lands in the same registry, so both styles are scraped, but this
// one does not depend on the exporter being enabled.
//
// A nil *Metrics is valid and every method is nil-safe, so a deployment that
// never wires them (tests, the CLI) runs unchanged.
package metrics

import (
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds the service's instruments.
type Metrics struct {
	// RunTotal counts agent runs by terminal status.
	RunTotal *prometheus.CounterVec
	// RunDuration measures how long a turn took, by engine.
	RunDuration *prometheus.HistogramVec
	// ToolTotal counts tool invocations by tool name and outcome. A tool name
	// comes from configuration, so it is a label rather than a separate metric:
	// the cardinality is bounded by what an operator configured, and the
	// per-tool view is the one worth having.
	ToolTotal *prometheus.CounterVec
	// ToolDuration measures one tool invocation.
	ToolDuration *prometheus.HistogramVec
	// TokensTotal counts model tokens by kind (prompt, completion, reasoning).
	TokensTotal *prometheus.CounterVec
	// ApprovalTotal counts human decisions on gated tool calls.
	ApprovalTotal *prometheus.CounterVec
	// SkillTotal counts skill backend builds, by provider and outcome.
	SkillTotal *prometheus.CounterVec
	// BlockedInputTotal counts messages refused by the inbound audit.
	// ImageTotal counts image operations, by operation and outcome.
	//
	// The counter that matters in practice is "inline": it says how often a
	// turn had to read an image to send it to a model, which is the cost this
	// design moved out of the conversation and into one place per turn. A
	// failure rate rising here is what a missing or full store looks like
	// before anyone reports an answer that ignored their screenshot.
	ImageTotal *prometheus.CounterVec
	// ImageBytes counts the bytes moved by image operations, by operation.
	ImageBytes        *prometheus.CounterVec
	BlockedInputTotal *prometheus.CounterVec
	// PanicTotal counts recovered panics, by where they happened. A non-zero
	// value is always a bug, which is exactly why it is worth a counter: an
	// error log can be missed, a series that should be flat is noticed.
	PanicTotal *prometheus.CounterVec
	// CompactionTotal counts history compactions by how they were done —
	// summarized or pruned. The two are not equivalent, and a deployment that
	// has quietly fallen back to pruning is losing context every turn.
	CompactionTotal *prometheus.CounterVec
	// WebhookTotal counts run-lifecycle deliveries by event, hook and outcome.
	// The hook name is a label for the same reason a tool name is: it comes from
	// configuration, so its cardinality is bounded by what an operator wrote.
	WebhookTotal *prometheus.CounterVec
	// WebhookDuration measures one delivery. onStart is on a run's critical
	// path, so a hook that is slow is a hook that is slow to answer everyone.
	WebhookDuration *prometheus.HistogramVec
	// ModelDuration measures one model call, by model and outcome. It is what
	// separates "the turn was slow" into "the model was slow": the run and tool
	// histograms cannot tell a slow provider from a slow tool.
	ModelDuration *prometheus.HistogramVec
	// ModelFirstToken measures how long a streaming call took to produce its
	// first chunk. That is the latency a person actually feels, and it is not
	// derivable from ModelDuration, which ends when the answer does.
	ModelFirstToken *prometheus.HistogramVec
	// CustomCommandTotal counts user-defined slash command invocations, by type
	// (prompt/http/tool) and phase (Succeeded/Denied/Failed). The command name is
	// deliberately not a label: it is user-authored, so its cardinality is
	// unbounded. The audit log carries the name instead.
	CustomCommandTotal *prometheus.CounterVec
	// CustomCommandDuration measures one user-defined command invocation.
	CustomCommandDuration *prometheus.HistogramVec
}

// M is the process-wide sink, set once at startup. Nil until then, which every
// method tolerates.
var M *Metrics

// Metric name prefixes. The legacy prefix is what this service has always
// emitted; renaming it is a breaking change for every dashboard and alert that
// queries these series, so the deployment decides — see New.
const (
	prefixAligned = "onexai_agent"
	prefixLegacy  = "agent"
)

// prefixFor selects the namespace New registers under. The default (false) is
// the legacy one, so a deployment that has not opted in keeps the exact metric
// names it had before this switch existed.
func prefixFor(aligned bool) string {
	if aligned {
		return prefixAligned
	}
	return prefixLegacy
}

// New registers the instruments on reg.
//
// aligned selects the metric namespace: true emits onexai_agent_*, false keeps
// the agent_* names this service has always used.
//
// A duplicate registration is tolerated rather than fatal, following the
// authorization package's precedent: promauto panics on one, and a collector
// colliding should never be the reason a server fails to start. If it does
// collide the sink is nil and everything continues without metrics.
func New(reg prometheus.Registerer, aligned bool) (m *Metrics) {
	prefix := prefixFor(aligned)

	factory := promauto.With(reg)
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("metrics: already registered; the service continues without its counters", "panic", r)
			m = nil
		}
	}()

	return &Metrics{
		RunTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_run_total",
			Help: "Total agent runs, by terminal status.",
		}, []string{"status"}),
		RunDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    prefix + "_run_duration_seconds",
			Help:    "Wall-clock time of one agent turn.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600},
		}, []string{"engine"}),
		ToolTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_tool_total",
			Help: "Total tool invocations, by tool and outcome.",
		}, []string{"tool", "status"}),
		ToolDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    prefix + "_tool_duration_seconds",
			Help:    "Wall-clock time of one tool invocation.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 30, 60, 300},
		}, []string{"tool"}),
		TokensTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_tokens_total",
			Help: "Model tokens consumed, by kind.",
		}, []string{"kind"}),
		ApprovalTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_approval_total",
			Help: "Human decisions on gated tool calls, by decision.",
		}, []string{"decision", "source"}),
		SkillTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_skill_build_total",
			Help: "Skill backend builds, by provider and outcome.",
		}, []string{"provider", "status"}),
		ImageTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_image_total",
			Help: "Image operations, by operation (upload/fetch/inline/generate) and outcome (ok/failed).",
		}, []string{"operation", "status"}),
		ImageBytes: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_image_bytes_total",
			Help: "Bytes moved by image operations, by operation.",
		}, []string{"operation"}),
		BlockedInputTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_blocked_input_total",
			Help: "Messages refused by the inbound audit, by category.",
		}, []string{"category"}),
		PanicTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_panic_total",
			Help: "Recovered panics, by where they happened.",
		}, []string{"where"}),
		WebhookTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_webhook_total",
			Help: "Run-lifecycle webhook deliveries, by event, hook and outcome.",
		}, []string{"event", "hook", "outcome"}),
		WebhookDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    prefix + "_webhook_duration_seconds",
			Help:    "Wall-clock time of one webhook delivery.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30},
		}, []string{"event"}),
		CompactionTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_history_compaction_total",
			Help: "History compactions, by method (summarized or pruned).",
		}, []string{"method"}),
		ModelDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    prefix + "_model_duration_seconds",
			Help:    "Wall-clock time of one model call, by model and outcome.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120},
		}, []string{"model", "status"}),
		ModelFirstToken: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    prefix + "_model_first_token_seconds",
			Help:    "Time from a streaming model call's start to its first chunk.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
		}, []string{"model"}),
		CustomCommandTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_custom_command_total",
			Help: "User-defined slash command invocations, by type and phase.",
		}, []string{"type", "phase"}),
		CustomCommandDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    prefix + "_custom_command_duration_seconds",
			Help:    "Wall-clock time of one user-defined command invocation.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60},
		}, []string{"type"}),
	}
}

// The methods below are nil-safe so no call site needs a guard.

// ObserveRun records one finished turn.
func (m *Metrics) ObserveRun(status, engine string, d time.Duration) {
	if m == nil {
		return
	}
	m.RunTotal.WithLabelValues(status).Inc()
	m.RunDuration.WithLabelValues(engine).Observe(d.Seconds())
}

// ObserveTool records one tool invocation.
func (m *Metrics) ObserveTool(tool, status string, d time.Duration) {
	if m == nil {
		return
	}
	m.ToolTotal.WithLabelValues(tool, status).Inc()
	m.ToolDuration.WithLabelValues(tool).Observe(d.Seconds())
}

// AddTokens records tokens by kind.
func (m *Metrics) AddTokens(kind string, n int) {
	if m == nil || n <= 0 {
		return
	}
	m.TokensTotal.WithLabelValues(kind).Add(float64(n))
}

// ObserveApproval records one human decision.
func (m *Metrics) ObserveApproval(decision, source string) {
	if m == nil {
		return
	}
	m.ApprovalTotal.WithLabelValues(decision, source).Inc()
}

// ObserveSkillBuild records one skill backend build.
func (m *Metrics) ObserveSkillBuild(provider, status string) {
	if m == nil {
		return
	}
	m.SkillTotal.WithLabelValues(provider, status).Inc()
}

// ObserveCustomCommand records one user-defined command invocation.
func (m *Metrics) ObserveCustomCommand(kind, phase string, d time.Duration) {
	if m == nil {
		return
	}
	m.CustomCommandTotal.WithLabelValues(kind, phase).Inc()
	m.CustomCommandDuration.WithLabelValues(kind).Observe(d.Seconds())
}

// ObserveImage records one image operation and the bytes it moved.
//
// The two are recorded together because a count without a size says nothing
// about whether a store is under pressure: a hundred thumbnails and a hundred
// screenshots are the same number and not the same bill.
func (m *Metrics) ObserveImage(operation, status string, bytes int64) {
	if m == nil {
		return
	}
	m.ImageTotal.WithLabelValues(operation, status).Inc()
	if bytes > 0 {
		m.ImageBytes.WithLabelValues(operation).Add(float64(bytes))
	}
}

// ObserveBlockedInput records one refused message.
func (m *Metrics) ObserveBlockedInput(category string) {
	if m == nil {
		return
	}
	m.BlockedInputTotal.WithLabelValues(category).Inc()
}

// ObservePanic records one recovered panic.
func (m *Metrics) ObservePanic(where string) {
	if m == nil {
		return
	}
	m.PanicTotal.WithLabelValues(where).Inc()
}

// ObserveCompaction records how a history was compacted.
func (m *Metrics) ObserveCompaction(method string) {
	if m == nil {
		return
	}
	m.CompactionTotal.WithLabelValues(method).Inc()
}

// ObserveModelCall records one model call.
func (m *Metrics) ObserveModelCall(model, status string, d time.Duration) {
	if m == nil {
		return
	}
	m.ModelDuration.WithLabelValues(model, status).Observe(d.Seconds())
}

// ObserveModelFirstToken records how long a streaming call took to produce its
// first chunk.
func (m *Metrics) ObserveModelFirstToken(model string, d time.Duration) {
	if m == nil {
		return
	}
	m.ModelFirstToken.WithLabelValues(model).Observe(d.Seconds())
}

// ObserveWebhook records one delivery: which event, which hook, how it ended,
// and how long it took.
//
// The outcome vocabulary is the one an operator alerts on. "refused" is a
// hook's own answer and is not a fault; "failed" is the endpoint not answering,
// which is what the hook's failure mode is about; "skipped" is the dispatcher
// declining to call a hook that has been failing (see the dispatcher's
// auto-disable). A dashboard that summed the last two would conflate "the
// endpoint is down" with "we stopped calling it".
func (m *Metrics) ObserveWebhook(event, hook, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.WebhookTotal.WithLabelValues(event, hook, outcome).Inc()
	m.WebhookDuration.WithLabelValues(event).Observe(d.Seconds())
}
