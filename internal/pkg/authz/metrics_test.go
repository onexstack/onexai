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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestNewMetricsToleratesADuplicateRegistration pins the startup behaviour: a
// collector name colliding must cost the counters, not the process. promauto
// panics on a duplicate, and this runs inside provider construction, which is
// on the path from "server starts" to "server does not start".
func TestNewMetricsToleratesADuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	if NewMetrics(reg) == nil {
		t.Fatal("the first registration must produce a sink")
	}
	if got := NewMetrics(reg); got != nil {
		t.Fatal("a duplicate registration must degrade to a nil sink rather than panic")
	}
}

// TestNilMetricsAreSafe keeps the degradation above honest: the sink it returns
// in place of a real one is nil, so every recording path has to tolerate that.
func TestNilMetricsAreSafe(t *testing.T) {
	var m *Metrics
	m.observeCheck("tool", "allow", "tool", "read", "opa", 0.01)
	m.observeDeny("tool", "tool", "read", "opa", "user")

	// The same path each evaluateLevels call takes, with the process-wide sink
	// never set.
	M.observeCheck("tool", "allow", "tool", "read", "opa", 0.01)
	M.observeDeny("tool", "tool", "read", "opa", "user")
}
