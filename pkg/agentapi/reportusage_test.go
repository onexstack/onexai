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

package agentapi

import "testing"

// TestReportsUsageUnsetMeansReport is the rolling-upgrade promise, and the reason
// the wire field is a tri-state rather than a bool.
//
// The platform ships its half first — it starts sending `false` before anything
// here reads it — so a caller that says nothing must keep the old behaviour:
// reported. Reading "unset" as false would silently stop metering every Lark run,
// which is the one kind of run nobody else records, and the symptom would be a
// ledger that is quietly short rather than an error.
func TestReportsUsageUnsetMeansReport(t *testing.T) {
	cases := []struct {
		name string
		req  AskRequest
		want bool
	}{
		{
			name: "a caller that never mentioned the field is reported (the Lark path)",
			req:  AskRequest{},
			want: true,
		},
		{
			name: "the platform says it meters this run itself",
			req:  AskRequest{Metering: MeteringCaller},
			want: false,
		},
		{
			name: "a caller that explicitly asks to be reported",
			req:  AskRequest{Metering: MeteringRuntime},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReportsUsage(&tc.req); got != tc.want {
				t.Fatalf("ReportsUsage(%+v) = %v, want %v", tc.req, got, tc.want)
			}
		})
	}
}
