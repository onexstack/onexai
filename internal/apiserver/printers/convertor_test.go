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

package printers_test

import (
	"context"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/onexstack/onexai/internal/apiserver/printers"
	"github.com/onexstack/onexai/pkg/apis/agent"
	"github.com/onexstack/onexai/pkg/apis/chat"
)

const (
	resourceVersion = "42"
	continueToken   = "next-page"
	remainingItems  = int64(3)
)

// columnCase is one resource's expected output: the columns in order, the
// priority that decides whether -o wide shows them, and the full row.
type columnCase struct {
	resource   string
	convertor  rest.TableConvertor
	object     runtime.Object
	columns    []string
	priorities []int32
	cells      []string
}

func readyConditions(conditionType string) []metav1.Condition {
	return []metav1.Condition{{
		Type:               conditionType,
		Status:             metav1.ConditionTrue,
		Reason:             "Accepted",
		Message:            "accepted",
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: 1,
	}}
}

func fillList(list metav1.ListInterface) {
	remaining := remainingItems
	list.SetResourceVersion(resourceVersion)
	list.SetContinue(continueToken)
	list.SetRemainingItemCount(&remaining)
}

func tableCases() []columnCase {
	// The second row of every list is an object with nothing but a name, so the
	// placeholder cells are asserted for all resources the same way.
	empty := func() metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: "empty"}
	}

	topK := int32(10)
	scoreThreshold := 0.7

	agentFull := &agent.Agent{ObjectMeta: metav1.ObjectMeta{Name: "kb-main", CreationTimestamp: metav1.Now()}}
	agentFull.Spec.Owner = &rbacv1.Subject{Name: "go.kong"}
	agentFull.Spec.DisplayName = "KB Main"
	agentFull.Status.Conditions = readyConditions("Ready")

	ruleFull := &agent.AuthorizationRule{ObjectMeta: metav1.ObjectMeta{Name: "allow-ops", CreationTimestamp: metav1.Now()}}
	ruleFull.Spec.Effect = agent.EffectAllow
	ruleFull.Status.Conditions = readyConditions("Loaded")

	knowledgeBaseFull := &agent.KnowledgeBase{
		ObjectMeta: metav1.ObjectMeta{Name: "kb-main", CreationTimestamp: metav1.Now()},
	}
	knowledgeBaseFull.Spec.Platform = "ragflow"
	knowledgeBaseFull.Spec.DatasetID = "ds-8831"
	knowledgeBaseFull.Spec.TopK = &topK
	knowledgeBaseFull.Spec.ScoreThreshold = &scoreThreshold
	knowledgeBaseFull.Status.Conditions = readyConditions("Ready")

	mcpServerFull := &agent.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "mcp-main", CreationTimestamp: metav1.Now()}}
	mcpServerFull.Spec.Protocol = "streamable-http"
	mcpServerFull.Status.Conditions = readyConditions("Ready")

	modelFull := &agent.Model{ObjectMeta: metav1.ObjectMeta{Name: "model-main", CreationTimestamp: metav1.Now()}}
	modelFull.Spec.Model = "deepseek-chat"
	modelFull.Status.Conditions = readyConditions("Ready")

	promptFull := &agent.Prompt{ObjectMeta: metav1.ObjectMeta{Name: "prompt-main", CreationTimestamp: metav1.Now()}}
	promptFull.Status.Conditions = readyConditions("Ready")

	skillFull := &agent.Skill{ObjectMeta: metav1.ObjectMeta{Name: "skill-main", CreationTimestamp: metav1.Now()}}
	skillFull.Spec.Mode = "http"
	skillFull.Spec.Provider = "builtin"
	skillFull.Status.Conditions = readyConditions("Ready")

	webhookFull := &agent.Webhook{ObjectMeta: metav1.ObjectMeta{Name: "webhook-main", CreationTimestamp: metav1.Now()}}
	webhookFull.Status.Conditions = readyConditions("Ready")

	botAppFull := &chat.BotApp{ObjectMeta: metav1.ObjectMeta{Name: "bot-main", CreationTimestamp: metav1.Now()}}
	botAppFull.Spec.AppID = "cli_a1b2"
	botAppFull.Spec.BotName = "onex"
	botAppFull.Spec.EventMode = "websocket"
	botAppFull.Status.Conditions = readyConditions("Ready")

	dutyGroupFull := &chat.DutyGroup{ObjectMeta: metav1.ObjectMeta{Name: "duty-main", CreationTimestamp: metav1.Now()}}
	dutyGroupFull.Spec.Agents = []string{"agent-a", "agent-b"}
	dutyGroupFull.Spec.DisplayName = "SRE"
	dutyGroupFull.Status.Conditions = readyConditions("Ready")

	peerBindingFull := &chat.PeerBinding{ObjectMeta: metav1.ObjectMeta{Name: "peer-main", CreationTimestamp: metav1.Now()}}
	peerBindingFull.Spec.Canonical = "onex:peer"
	peerBindingFull.Spec.PeerID = "peer-1"
	peerBindingFull.Spec.Agents = []string{"agent-a"}
	peerBindingFull.Status.Conditions = readyConditions("Ready")

	agentList := &agent.AgentList{Items: []agent.Agent{*agentFull, {ObjectMeta: empty()}}}
	fillList(agentList)

	ruleList := &agent.AuthorizationRuleList{Items: []agent.AuthorizationRule{*ruleFull, {ObjectMeta: empty()}}}
	fillList(ruleList)

	knowledgeBaseList := &agent.KnowledgeBaseList{Items: []agent.KnowledgeBase{*knowledgeBaseFull, {ObjectMeta: empty()}}}
	fillList(knowledgeBaseList)

	mcpServerList := &agent.MCPServerList{Items: []agent.MCPServer{*mcpServerFull, {ObjectMeta: empty()}}}
	fillList(mcpServerList)

	modelList := &agent.ModelList{Items: []agent.Model{*modelFull, {ObjectMeta: empty()}}}
	fillList(modelList)

	promptList := &agent.PromptList{Items: []agent.Prompt{*promptFull, {ObjectMeta: empty()}}}
	fillList(promptList)

	skillList := &agent.SkillList{Items: []agent.Skill{*skillFull, {ObjectMeta: empty()}}}
	fillList(skillList)

	webhookList := &agent.WebhookList{Items: []agent.Webhook{*webhookFull, {ObjectMeta: empty()}}}
	fillList(webhookList)

	botAppList := &chat.BotAppList{Items: []chat.BotApp{*botAppFull, {ObjectMeta: empty()}}}
	fillList(botAppList)

	dutyGroupList := &chat.DutyGroupList{Items: []chat.DutyGroup{*dutyGroupFull, {ObjectMeta: empty()}}}
	fillList(dutyGroupList)

	peerBindingList := &chat.PeerBindingList{Items: []chat.PeerBinding{*peerBindingFull, {ObjectMeta: empty()}}}
	fillList(peerBindingList)

	return []columnCase{
		{
			resource:   "agents",
			convertor:  printers.Agents(),
			object:     agentList,
			columns:    []string{"Name", "Status", "Owner", "Display Name", "Age"},
			priorities: []int32{0, 0, 0, 1, 0},
			cells:      []string{"kb-main", "Ready", "go.kong", "KB Main", "0s"},
		},
		{
			resource:   "authorizationrules",
			convertor:  printers.AuthorizationRules(),
			object:     ruleList,
			columns:    []string{"Name", "Status", "Effect", "Age"},
			priorities: []int32{0, 0, 0, 0},
			cells:      []string{"allow-ops", "Ready", "Allow", "0s"},
		},
		{
			resource:   "knowledgebases",
			convertor:  printers.KnowledgeBases(),
			object:     knowledgeBaseList,
			columns:    []string{"Name", "Status", "Platform", "Dataset ID", "Top K", "Score Threshold", "Age"},
			priorities: []int32{0, 0, 0, 0, 1, 1, 0},
			cells:      []string{"kb-main", "Ready", "ragflow", "ds-8831", "10", "0.7", "0s"},
		},
		{
			resource:   "mcpservers",
			convertor:  printers.MCPServers(),
			object:     mcpServerList,
			columns:    []string{"Name", "Status", "Protocol", "Age"},
			priorities: []int32{0, 0, 0, 0},
			cells:      []string{"mcp-main", "Ready", "streamable-http", "0s"},
		},
		{
			resource:   "models",
			convertor:  printers.Models(),
			object:     modelList,
			columns:    []string{"Name", "Status", "Model", "Age"},
			priorities: []int32{0, 0, 0, 0},
			cells:      []string{"model-main", "Ready", "deepseek-chat", "0s"},
		},
		{
			resource:   "prompts",
			convertor:  printers.Prompts(),
			object:     promptList,
			columns:    []string{"Name", "Status", "Age"},
			priorities: []int32{0, 0, 0},
			cells:      []string{"prompt-main", "Ready", "0s"},
		},
		{
			resource:   "skills",
			convertor:  printers.Skills(),
			object:     skillList,
			columns:    []string{"Name", "Status", "Mode", "Provider", "Age"},
			priorities: []int32{0, 0, 0, 0, 0},
			cells:      []string{"skill-main", "Ready", "http", "builtin", "0s"},
		},
		{
			resource:   "webhooks",
			convertor:  printers.Webhooks(),
			object:     webhookList,
			columns:    []string{"Name", "Status", "Age"},
			priorities: []int32{0, 0, 0},
			cells:      []string{"webhook-main", "Ready", "0s"},
		},
		{
			resource:   "botapps",
			convertor:  printers.BotApps(),
			object:     botAppList,
			columns:    []string{"Name", "Status", "App ID", "Bot Name", "Event Mode", "Age"},
			priorities: []int32{0, 0, 0, 0, 1, 0},
			cells:      []string{"bot-main", "Ready", "cli_a1b2", "onex", "websocket", "0s"},
		},
		{
			resource:   "dutygroups",
			convertor:  printers.DutyGroups(),
			object:     dutyGroupList,
			columns:    []string{"Name", "Status", "Agents", "Display Name", "Age"},
			priorities: []int32{0, 0, 0, 0, 0},
			cells:      []string{"duty-main", "Ready", "agent-a,agent-b", "SRE", "0s"},
		},
		{
			resource:   "peerbindings",
			convertor:  printers.PeerBindings(),
			object:     peerBindingList,
			columns:    []string{"Name", "Status", "Canonical", "Peer ID", "Agents", "Age"},
			priorities: []int32{0, 0, 0, 0, 0, 0},
			cells:      []string{"peer-main", "Ready", "onex:peer", "peer-1", "agent-a", "0s"},
		},
	}
}

func TestConvertorColumns(t *testing.T) {
	t.Parallel()

	for _, tc := range tableCases() {
		t.Run(tc.resource, func(t *testing.T) {
			t.Parallel()

			table, err := tc.convertor.ConvertToTable(context.Background(), tc.object, nil)
			if err != nil {
				t.Fatalf("ConvertToTable: %v", err)
			}

			assertColumns(t, table.ColumnDefinitions, tc.columns, tc.priorities)

			if len(table.Rows) != 2 {
				t.Fatalf("got %d rows, want 2", len(table.Rows))
			}

			assertCells(t, table.Rows[0].Cells, tc.cells)
			assertEmptyCells(t, table.Rows[1].Cells, len(tc.columns))
			assertRowObjects(t, table.Rows)

			if table.ResourceVersion != resourceVersion {
				t.Errorf("resourceVersion = %q, want %q", table.ResourceVersion, resourceVersion)
			}

			if table.Continue != continueToken {
				t.Errorf("continue = %q, want %q", table.Continue, continueToken)
			}

			if table.RemainingItemCount == nil || *table.RemainingItemCount != remainingItems {
				t.Errorf("remainingItemCount = %v, want %d", table.RemainingItemCount, remainingItems)
			}
		})
	}
}

func TestConvertorNoHeaders(t *testing.T) {
	t.Parallel()

	convertor := printers.Models()
	table, err := convertor.ConvertToTable(
		context.Background(),
		&agent.ModelList{},
		&metav1.TableOptions{NoHeaders: true},
	)
	if err != nil {
		t.Fatalf("ConvertToTable: %v", err)
	}

	if len(table.ColumnDefinitions) != 0 {
		t.Errorf("got %d column definitions with NoHeaders, want 0", len(table.ColumnDefinitions))
	}
}

func TestConvertorSingleObject(t *testing.T) {
	t.Parallel()

	model := &agent.Model{ObjectMeta: metav1.ObjectMeta{Name: "model-main"}}
	model.Spec.Model = "deepseek-chat"

	table, err := printers.Models().ConvertToTable(context.Background(), model, nil)
	if err != nil {
		t.Fatalf("ConvertToTable: %v", err)
	}

	if len(table.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(table.Rows))
	}

	if got := table.Rows[0].Cells[0]; got != "model-main" {
		t.Errorf("name cell = %v, want model-main", got)
	}

	if table.ResourceVersion != "" {
		t.Errorf("resourceVersion = %q, want empty", table.ResourceVersion)
	}
}

func TestConvertorRejectsUnknownObject(t *testing.T) {
	t.Parallel()

	// An empty list of another kind cannot be told apart from an empty list of
	// the right kind, so the foreign list carries an item.
	foreign := &agent.PromptList{Items: []agent.Prompt{{ObjectMeta: metav1.ObjectMeta{Name: "prompt-main"}}}}

	_, err := printers.Models().ConvertToTable(context.Background(), foreign, nil)
	if err == nil {
		t.Fatal("ConvertToTable accepted a list of another kind")
	}
}

func TestConvertorRejectsNil(t *testing.T) {
	t.Parallel()

	_, err := printers.Models().ConvertToTable(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("ConvertToTable accepted a nil object")
	}
}

func assertColumns(
	t *testing.T,
	defs []metav1.TableColumnDefinition,
	wantColumns []string,
	wantPriorities []int32,
) {
	t.Helper()

	if len(defs) != len(wantColumns) {
		t.Fatalf("got %d columns, want %d", len(defs), len(wantColumns))
	}

	for i, def := range defs {
		if def.Name != wantColumns[i] {
			t.Errorf("column %d = %q, want %q", i, def.Name, wantColumns[i])
		}

		if def.Priority != wantPriorities[i] {
			t.Errorf("column %q priority = %d, want %d", def.Name, def.Priority, wantPriorities[i])
		}
	}
}

func assertCells(t *testing.T, cells []any, want []string) {
	t.Helper()

	if len(cells) != len(want) {
		t.Fatalf("got %d cells, want %d: %v", len(cells), len(want), cells)
	}

	for i, cell := range cells {
		if cell != want[i] {
			t.Errorf("cell %d = %v, want %q", i, cell, want[i])
		}
	}
}

// assertEmptyCells checks the row of an object that sets nothing but its name.
func assertEmptyCells(t *testing.T, cells []any, columns int) {
	t.Helper()

	if len(cells) != columns {
		t.Fatalf("got %d cells, want %d", len(cells), columns)
	}

	if cells[0] != "empty" {
		t.Errorf("name cell = %v, want empty", cells[0])
	}

	if cells[1] != "Unknown" {
		t.Errorf("status cell = %v, want Unknown", cells[1])
	}

	for i := 2; i < len(cells)-1; i++ {
		if cells[i] != "<none>" {
			t.Errorf("cell %d = %v, want <none>", i, cells[i])
		}
	}

	if last := cells[len(cells)-1]; last != "<unknown>" {
		t.Errorf("age cell = %v, want <unknown>", last)
	}
}

func assertRowObjects(t *testing.T, rows []metav1.TableRow) {
	t.Helper()

	for i, row := range rows {
		if row.Object.Object == nil {
			t.Fatalf("row %d carries no object", i)
		}

		if _, err := apimeta.Accessor(row.Object.Object); err != nil {
			t.Fatalf("row %d object is not addressable: %v", i, err)
		}
	}
}
