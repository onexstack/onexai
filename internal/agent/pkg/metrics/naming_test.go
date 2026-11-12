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

package metrics

import "testing"

// TestPrefixForPinsLegacyDefault is the regression guard for the metric-name
// switch: with the switch off the service must keep emitting the names it has
// always emitted. Anything else silently breaks every dashboard and alert that
// queries them, and it would look like a metrics outage rather than a rename.
func TestPrefixForPinsLegacyDefault(t *testing.T) {
	if got := prefixFor(false); got != "agent" {
		t.Errorf("prefixFor(false) = %q, want the legacy prefix %q", got, "agent")
	}
	if got := prefixFor(true); got != "onexai_agent" {
		t.Errorf("prefixFor(true) = %q, want the aligned prefix %q", got, "onexai_agent")
	}
}
