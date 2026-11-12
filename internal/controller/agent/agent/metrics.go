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

package agent

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// refsUnresolvedTotal counts references the controller could not resolve. A
// growing series is the signal an operator needs: the effective view is smaller
// than the spec, and the reason says which side to fix.
var refsUnresolvedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "onexai_agent_refs_unresolved_total",
	Help: "Agent references that did not resolve, by kind and reason.",
}, []string{"kind", "reason"})

func init() {
	// A duplicate registration must never keep the controller manager from
	// starting; the mirror and controller metrics use the same registry.
	defer func() { _ = recover() }()
	crmetrics.Registry.MustRegister(refsUnresolvedTotal)
}

// recordUnresolved updates the metric from the controller's missing list. The
// list is free-form ("Skill/platform/x (namespace not allowed)"), so the kind
// is the prefix before the first slash and the reason is the suffix.
func recordUnresolved(missing []string) {
	for _, item := range missing {
		kind := item
		if at := strings.IndexByte(kind, '/'); at > 0 {
			kind = kind[:at]
		}
		reason := "NotFound"
		switch {
		case strings.Contains(item, "namespace not allowed"):
			reason = "NotPermitted"
		case strings.Contains(item, "disabled"):
			reason = "Disabled"
		}
		refsUnresolvedTotal.WithLabelValues(kind, reason).Inc()
	}
}
