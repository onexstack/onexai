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

import (
	"strings"
	"time"

	"github.com/spf13/pflag"
)

// DutyOptions selects the on-call/incident backend and the escalation worker.
type DutyOptions struct {
	// Provider is "none" or "regen". An empty value infers "regen" when
	// backend.regen-endpoint is set, which keeps existing deployments working.
	// Native schedules do not use the provider.
	Provider string `json:"provider" mapstructure:"provider"`

	// EscalationWorkerEnabled starts the background escalation worker. It is
	// off by default: the worker pages people, and a deployment must opt in
	// after verifying the Lark DM permission and the on-call name map.
	EscalationWorkerEnabled bool `json:"escalation-worker-enabled" mapstructure:"escalation-worker-enabled"`
	// EscalationInterval is how often the worker scans due escalations.
	EscalationInterval time.Duration `json:"escalation-interval" mapstructure:"escalation-interval"`
}

// NewDutyOptions creates DutyOptions with defaults.
func NewDutyOptions() *DutyOptions {
	return &DutyOptions{EscalationInterval: 30 * time.Second}
}

// AddFlags binds the duty options to a flag set.
func (o *DutyOptions) AddFlags(fs *pflag.FlagSet, prefix string) {
	fs.StringVar(&o.Provider, prefix+".provider", o.Provider,
		"Duty provider: none or regen. Empty infers regen when backend.regen-endpoint is set. Native schedules do not use it.")
	fs.BoolVar(&o.EscalationWorkerEnabled, prefix+".escalation-worker-enabled", o.EscalationWorkerEnabled,
		"Run the background ticket escalation worker. Off by default; manual escalation still works.")
	fs.DurationVar(&o.EscalationInterval, prefix+".escalation-interval", o.EscalationInterval,
		"How often the escalation worker scans due escalations.")
}

// EffectiveProvider resolves the configured provider name. It never returns an
// empty string; an unknown name is treated as "none" by the caller.
func (o *DutyOptions) EffectiveProvider(backend *BackendOptions) string {
	if o != nil {
		if p := strings.ToLower(strings.TrimSpace(o.Provider)); p != "" {
			return p
		}
	}
	if backend != nil && strings.TrimSpace(backend.RegenEndpoint) != "" {
		return "regen"
	}
	return "none"
}
