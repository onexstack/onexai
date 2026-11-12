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
	"context"
	"testing"

	"github.com/onexstack/onexai/internal/agent/pkg/usage"
	"github.com/onexstack/onexai/internal/agent/pkg/usagereport"
	agent "github.com/onexstack/onexai/pkg/agentapi"
)

// countingReporter records what it was asked to report.
type countingReporter struct {
	records []usagereport.Record
}

func (r *countingReporter) Report(_ context.Context, rec usagereport.Record) {
	r.records = append(r.records, rec)
}

// TestARunThePlatformMetersIsNotReportedAgain pins the one line that stops a 2x
// charge.
//
// The platform writes the ledger rows for the runs it starts — in the same
// transaction as the message or the comment the run produced — and it says so by
// asking with `reportUsage: false`. Nothing about the run itself distinguishes it
// from one the Lark path starts: same sender, same session key, same totals. So
// without this gate both sides record it, and the ledger carries two rows that
// agree on every column but the id. Measured 2026-10-13: 236 runs already did.
func TestARunThePlatformMetersIsNotReportedAgain(t *testing.T) {
	reporter := &countingReporter{}
	e := &einoEngine{reporter: reporter}

	run := agent.RunOptions{
		SenderID:   "user-1",
		SessionKey: "cmt-reply:cmt-1:user-1",
		RunID:      "run-1",
	}
	totals := usage.Totals{Calls: 2, PromptTokens: 100, CompletionTokens: 20}

	// What the platform sends: it meters this run itself.
	e.reportUsage(context.Background(), withReportUsage(run, false), totals)
	if len(reporter.records) != 0 {
		t.Fatalf("a run the platform meters was reported again: %+v", reporter.records)
	}

	// What a caller that says nothing asks for — the Lark path.
	e.reportUsage(context.Background(), withReportUsage(run, true), totals)
	if len(reporter.records) != 1 {
		t.Fatalf("a run nobody else meters must be reported exactly once, got %d", len(reporter.records))
	}
	got := reporter.records[0]
	if got.RunID != "run-1" || got.UserID != "user-1" || got.SessionKey != "cmt-reply:cmt-1:user-1" {
		t.Fatalf("the report lost the run's identity: %+v", got)
	}
}

// withReportUsage sets the resolved flag on options that are otherwise the same,
// so the two calls above differ in exactly the one thing under test.
func withReportUsage(opts agent.RunOptions, report bool) agent.RunOptions {
	opts.ReportUsage = report
	return opts
}
