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

package validation

import (
	"context"
	"testing"
	"time"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func TestValidateListAdminRunQuery(t *testing.T) {
	v := &Validator{}
	ctx := context.Background()

	valid := &v1.ListAdminRunQuery{Offset: 0, Limit: 20, State: "failed", Trigger: "scheduled"}
	if err := v.ValidateListAdminRunQuery(ctx, valid); err != nil {
		t.Fatalf("valid query refused: %v", err)
	}
	windowed := &v1.ListAdminRunQuery{
		Offset: 0, Limit: 20, From: 1_760_000_000_000, To: 1_760_003_600_000,
	}
	if err := v.ValidateListAdminRunQuery(ctx, windowed); err != nil {
		t.Fatalf("valid window refused: %v", err)
	}

	cases := []struct {
		name  string
		query *v1.ListAdminRunQuery
	}{
		{"negative offset", &v1.ListAdminRunQuery{Offset: -1}},
		{"oversized page", &v1.ListAdminRunQuery{Limit: 201}},
		{"unknown state", &v1.ListAdminRunQuery{State: "completed"}},
		{"unknown trigger", &v1.ListAdminRunQuery{Trigger: "cron"}},
		{"negative from", &v1.ListAdminRunQuery{From: -1, To: 1_760_000_000_000}},
		{"lone from", &v1.ListAdminRunQuery{From: 1_760_000_000_000}},
		{"lone to", &v1.ListAdminRunQuery{To: 1_760_000_000_000}},
		{"to not after from", &v1.ListAdminRunQuery{From: 1_760_000_000_000, To: 1_760_000_000_000}},
		{"window longer than seven days", &v1.ListAdminRunQuery{
			From: 1_760_000_000_000, To: 1_760_000_000_000 + 8*24*3_600_000,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.ValidateListAdminRunQuery(ctx, tc.query); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestValidateAdminRunIdentifiers(t *testing.T) {
	v := &Validator{}
	ctx := context.Background()

	if err := v.ValidateGetAdminRunRequest(ctx, &v1.GetAdminRunRequest{}); err == nil {
		t.Fatal("empty get id must be refused")
	}
	if err := v.ValidateCancelAdminRunRequest(ctx, &v1.CancelAdminRunRequest{}); err == nil {
		t.Fatal("empty cancel id must be refused")
	}
	if err := v.ValidateRetryAdminRunRequest(ctx, &v1.RetryAdminRunRequest{}); err == nil {
		t.Fatal("empty retry id must be refused")
	}

	if err := v.ValidateListAdminRunEventRequest(ctx, &v1.ListAdminRunEventQuery{ID: "r", After: -1}); err == nil {
		t.Fatal("negative cursor must be refused")
	}
	if err := v.ValidateListAdminRunEventRequest(ctx, &v1.ListAdminRunEventQuery{ID: "r", Limit: 501}); err == nil {
		t.Fatal("oversized event page must be refused")
	}
	if err := v.ValidateListAdminRunEventRequest(ctx, &v1.ListAdminRunEventQuery{ID: "r", Limit: 500}); err != nil {
		t.Fatalf("valid event query refused: %v", err)
	}
}

func TestValidateListAdminRunMonitorQuery(t *testing.T) {
	v := &Validator{}
	ctx := context.Background()
	from := int64(1_760_000_000_000)
	to := from + v1.MonitorStep1h*1

	valid := &v1.ListAdminRunMonitorQuery{
		Agent: "ops-agent", From: from, To: to,
		Step: v1.MonitorStep1m, Limit: 10,
	}
	if err := v.ValidateListAdminRunMonitorQuery(ctx, valid); err != nil {
		t.Fatalf("valid query refused: %v", err)
	}

	fleet := &v1.ListAdminRunMonitorQuery{
		All: true, From: from, To: to, Step: v1.MonitorStep1m, Limit: 10,
	}
	if err := v.ValidateListAdminRunMonitorQuery(ctx, fleet); err != nil {
		t.Fatalf("explicit fleet query refused: %v", err)
	}

	cases := []struct {
		name  string
		query *v1.ListAdminRunMonitorQuery
	}{
		{"empty agent without all", &v1.ListAdminRunMonitorQuery{From: from, To: to, Step: v1.MonitorStep1m}},
		{"agent and all together", &v1.ListAdminRunMonitorQuery{
			Agent: "a", All: true, From: from, To: to, Step: v1.MonitorStep1m,
		}},
		{"negative from", &v1.ListAdminRunMonitorQuery{Agent: "a", From: -1, To: to, Step: v1.MonitorStep1m}},
		{"to not after from", &v1.ListAdminRunMonitorQuery{Agent: "a", From: to, To: to, Step: v1.MonitorStep1m}},
		{"window longer than seven days", &v1.ListAdminRunMonitorQuery{
			Agent: "a", From: from - 8*24*v1.MonitorStep1h, To: to, Step: v1.MonitorStep1h,
		}},
		{"unsupported step", &v1.ListAdminRunMonitorQuery{
			Agent: "a", From: from, To: to, Step: 1234,
		}},
		{"too many buckets", &v1.ListAdminRunMonitorQuery{
			Agent: "a", From: from - 7*24*v1.MonitorStep1h, To: to, Step: v1.MonitorStep1m,
		}},
		{"oversized limit", &v1.ListAdminRunMonitorQuery{
			Agent: "a", From: from, To: to, Step: v1.MonitorStep1m, Limit: 51,
		}},
		{"negative limit", &v1.ListAdminRunMonitorQuery{
			Agent: "a", From: from, To: to, Step: v1.MonitorStep1m, Limit: -1,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.ValidateListAdminRunMonitorQuery(ctx, tc.query); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestValidateListAdminRunStuckQuery(t *testing.T) {
	v := &Validator{}
	ctx := context.Background()

	for _, query := range []*v1.ListAdminRunStuckQuery{
		{},
		{Agent: "ops-agent", Limit: 20},
		{Limit: v1.StuckMaxLimit},
	} {
		if err := v.ValidateListAdminRunStuckQuery(ctx, query); err != nil {
			t.Fatalf("valid stuck query refused: %v", err)
		}
	}

	for _, query := range []*v1.ListAdminRunStuckQuery{
		{Limit: -1},
		{Limit: v1.StuckMaxLimit + 1},
	} {
		if err := v.ValidateListAdminRunStuckQuery(ctx, query); err == nil {
			t.Fatalf("stuck query %+v must be refused", query)
		}
	}
}

func TestListAdminRunMonitorQueryDefault(t *testing.T) {
	q := &v1.ListAdminRunMonitorQuery{Agent: "ops-agent"}
	q.Default()
	if q.To <= 0 || q.From != q.To-v1.MonitorDefaultWindowMs || q.Step != v1.MonitorStep1m || q.Limit != v1.MonitorDefaultLimit {
		t.Fatalf("defaults = %+v, want a one-hour window with one-minute buckets", q)
	}

	cases := []struct {
		span int64
		want int64
	}{
		{v1.MonitorStep1h, v1.MonitorStep1m},
		{v1.MonitorStep1h * 6, v1.MonitorStep5m},
		{v1.MonitorStep1h * 24, v1.MonitorStep30m},
		{v1.MonitorStep1h * 24 * 7, v1.MonitorStep1h},
	}
	for _, tc := range cases {
		to := time.Now().UnixMilli()
		query := &v1.ListAdminRunMonitorQuery{
			Agent: "ops-agent", From: to - tc.span, To: to,
		}
		query.Default()
		if query.Step != tc.want {
			t.Fatalf("span %d defaulted to step %d, want %d", tc.span, query.Step, tc.want)
		}
	}
}
