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

package options

import "github.com/spf13/pflag"

// FeatureOptions gates the implementation-alignment work with os-apiserver.
//
// Every field defaults to false, and each one is off until it has been
// verified in a real deployment: the switches exist so that a release can
// carry the new code without changing any observable behaviour. When all of
// them are false the services behave exactly as they did before the switch
// was introduced — same routes, same response bodies, same metric names.
type FeatureOptions struct {
	// Pagination turns on the strict list contract: List returns
	// (count, items, error) and the proto requests carry Offset/Limit. While
	// off, list endpoints keep returning every record, exactly as before.
	Pagination bool `json:"pagination" mapstructure:"pagination"`
	// ObservabilitySkip widens the request-observability middleware for the
	// streaming, long-lived and high-volume routes (WebSocket, Lark webhook,
	// SSE, A2A): their bodies are no longer logged and their requests no longer
	// trip the body-capturing writer.
	ObservabilitySkip bool `json:"observability-skip" mapstructure:"observability-skip"`
	// UnifiedPanic routes background goroutines through the shared panics
	// helper, which also reports the panic as a metric.
	UnifiedPanic bool `json:"unified-panic" mapstructure:"unified-panic"`
	// AlignedMetricNames makes the metrics layer emit the aligned
	// onexai_<service>_* names instead of the pre-alignment ones (opsassist_*
	// for chat, agent_* for agent). Metric names are part of a dashboard's
	// contract, so the rename is a decision to flip once the dashboards and
	// alerts that query them have been updated — not a side effect of this
	// release.
	AlignedMetricNames bool `json:"aligned-metric-names" mapstructure:"aligned-metric-names"`
}

// NewFeatureOptions creates a FeatureOptions with all switches off.
func NewFeatureOptions() *FeatureOptions {
	return &FeatureOptions{}
}

// AddFlags binds FeatureOptions to command-line flags.
func (o *FeatureOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.BoolVar(&o.Pagination, fullPrefix+".pagination", o.Pagination,
		"Use the (count, items) list contract with Offset/Limit; off returns every record.")
	fs.BoolVar(&o.ObservabilitySkip, fullPrefix+".observability-skip", o.ObservabilitySkip,
		"Skip body capture and body logging on streaming/long-lived routes.")
	fs.BoolVar(&o.UnifiedPanic, fullPrefix+".unified-panic", o.UnifiedPanic,
		"Recover background-goroutine panics through the shared helper and report them as a metric.")
	fs.BoolVar(&o.AlignedMetricNames, fullPrefix+".aligned-metric-names", o.AlignedMetricNames,
		"Emit onexai_<service>_* metric names instead of the pre-alignment ones.")
}

// Validate checks whether the FeatureOptions are valid. Every switch is
// independent, so there is nothing to cross-check today; the method exists so
// a future switch that depends on another option has an obvious home.
func (o *FeatureOptions) Validate() []error {
	return nil
}
