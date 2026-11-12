// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package printers

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/pkg/apis/agent"
)

func statusCell(t *testing.T, obj *agent.Agent) string {
	t.Helper()

	table, err := agentTable.ConvertToTable(context.Background(), obj, nil)
	if err != nil {
		t.Fatalf("ConvertToTable: %v", err)
	}

	return table.Rows[0].Cells[1].(string)
}

func TestHumanAge(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name    string
		created metav1.Time
		want    string
	}{
		{name: "zero", created: metav1.Time{}, want: unknownAge},
		{name: "seconds", created: metav1.NewTime(now.Add(-30 * time.Second)), want: "30s"},
		{name: "days", created: metav1.NewTime(now.Add(-72 * time.Hour)), want: "3d"},
		{name: "future", created: metav1.NewTime(now.Add(time.Hour)), want: "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := humanAge(tt.created, now); got != tt.want {
				t.Errorf("humanAge() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConditionStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		conditions []metav1.Condition
		want       string
	}{
		{name: "missing", conditions: nil, want: statusUnknown},
		{
			name:       "true",
			conditions: []metav1.Condition{{Type: conditionReady, Status: metav1.ConditionTrue}},
			want:       statusReady,
		},
		{
			name:       "false",
			conditions: []metav1.Condition{{Type: conditionReady, Status: metav1.ConditionFalse}},
			want:       statusNotReady,
		},
		{
			name:       "unknown",
			conditions: []metav1.Condition{{Type: conditionReady, Status: metav1.ConditionUnknown}},
			want:       statusUnknown,
		},
		{
			name:       "other condition only",
			conditions: []metav1.Condition{{Type: "RefsResolved", Status: metav1.ConditionTrue}},
			want:       statusUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := conditionStatus(tt.conditions, conditionReady); got != tt.want {
				t.Errorf("conditionStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusColumnPrefersDeletion(t *testing.T) {
	t.Parallel()

	deleting := &agent.Agent{ObjectMeta: metav1.ObjectMeta{
		Name:              "going-away",
		DeletionTimestamp: &metav1.Time{Time: time.Now()},
	}}
	deleting.Status.Conditions = []metav1.Condition{{Type: conditionReady, Status: metav1.ConditionTrue}}

	if got := statusCell(t, deleting); got != terminating {
		t.Errorf("status cell = %q, want %q", got, terminating)
	}
}

func TestAuthorizationRuleStatusUsesLoaded(t *testing.T) {
	t.Parallel()

	rejected := &agent.AuthorizationRule{ObjectMeta: metav1.ObjectMeta{Name: "rejected"}}
	rejected.Status.Conditions = []metav1.Condition{{Type: conditionLoaded, Status: metav1.ConditionFalse}}

	table, err := authorizationRuleTable.ConvertToTable(context.Background(), rejected, nil)
	if err != nil {
		t.Fatalf("ConvertToTable: %v", err)
	}

	if got := table.Rows[0].Cells[1]; got != statusNotReady {
		t.Errorf("status cell = %v, want %q", got, statusNotReady)
	}

	readyOnly := &agent.AuthorizationRule{ObjectMeta: metav1.ObjectMeta{Name: "ready-only"}}
	readyOnly.Status.Conditions = []metav1.Condition{{Type: conditionReady, Status: metav1.ConditionTrue}}

	table, err = authorizationRuleTable.ConvertToTable(context.Background(), readyOnly, nil)
	if err != nil {
		t.Fatalf("ConvertToTable: %v", err)
	}

	if got := table.Rows[0].Cells[1]; got != statusUnknown {
		t.Errorf("status cell = %v, want %q", got, statusUnknown)
	}
}

func TestOrNone(t *testing.T) {
	t.Parallel()

	if got := orNone(""); got != emptyCell {
		t.Errorf("orNone(\"\") = %q, want %q", got, emptyCell)
	}

	if got := orNone("kb-main"); got != "kb-main" {
		t.Errorf("orNone(kb-main) = %q, want kb-main", got)
	}
}

func TestJoinNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{name: "nil", names: nil, want: emptyCell},
		{name: "blank entries", names: []string{"", ""}, want: emptyCell},
		{name: "skips blank entries", names: []string{"a", "", "b"}, want: "a,b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := joinNames(tt.names); got != tt.want {
				t.Errorf("joinNames() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOptionalNumbers(t *testing.T) {
	t.Parallel()

	topK := int32(10)
	if got := optInt32(nil); got != emptyCell {
		t.Errorf("optInt32(nil) = %q, want %q", got, emptyCell)
	}

	if got := optInt32(&topK); got != "10" {
		t.Errorf("optInt32(10) = %q, want 10", got)
	}

	score := 0.75
	if got := optFloat64(nil); got != emptyCell {
		t.Errorf("optFloat64(nil) = %q, want %q", got, emptyCell)
	}

	if got := optFloat64(&score); got != "0.75" {
		t.Errorf("optFloat64(0.75) = %q, want 0.75", got)
	}
}
