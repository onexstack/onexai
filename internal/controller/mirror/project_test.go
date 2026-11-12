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

package mirror

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func testAgent() *agentv1alpha1.Agent {
	return &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "helper",
			Namespace:       "default",
			UID:             "uid-1",
			ResourceVersion: "42",
			Generation:      3,
			Labels:          map[string]string{"team": "sre"},
		},
		Spec: agentv1alpha1.AgentSpec{
			CommonSpec: agentv1alpha1.CommonSpec{
				Enabled:     true,
				DisplayName: "Helper",
			},
		},
	}
}

func agentKind() Kind {
	for _, kind := range Registry() {
		if kind.Kind == "Agent" {
			return kind
		}
	}
	panic("Agent is not in the registry")
}

func TestProjectPromotesTheCommonFilters(t *testing.T) {
	now := time.Now()
	row, err := Project(agentKind(), testAgent(), now)
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	if row.Group != agentv1alpha1.GroupName || row.Kind != "Agent" {
		t.Fatalf("identity = %s/%s", row.Group, row.Kind)
	}
	if row.Namespace != "default" || row.Name != "helper" {
		t.Fatalf("name = %s/%s", row.Namespace, row.Name)
	}
	if row.ResourceVersion != 42 || row.Generation != 3 {
		t.Fatalf("resourceVersion/generation = %d/%d", row.ResourceVersion, row.Generation)
	}
	if row.Enabled == nil || !*row.Enabled {
		t.Fatalf("enabled was not promoted: %v", row.Enabled)
	}
	if row.DisplayName == nil || *row.DisplayName != "Helper" {
		t.Fatalf("displayName was not promoted: %v", row.DisplayName)
	}
	if string(row.Labels) != `{"team":"sre"}` {
		t.Fatalf("labels = %s", row.Labels)
	}
	if !row.ObservedAt.Equal(now) {
		t.Fatalf("observedAt = %s, want %s", row.ObservedAt, now)
	}
}

func TestProjectHashesSplitSpecFromStatus(t *testing.T) {
	kind := agentKind()
	before, err := Project(kind, testAgent(), time.Now())
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	statusOnly := testAgent()
	statusOnly.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ok"}}
	statusRow, err := Project(kind, statusOnly, time.Now())
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if statusRow.SpecHash != before.SpecHash {
		t.Fatal("a status change moved the spec hash")
	}
	if statusRow.StatusHash == before.StatusHash {
		t.Fatal("a status change left the status hash alone")
	}

	specOnly := testAgent()
	specOnly.Spec.DisplayName = "Renamed"
	specRow, err := Project(kind, specOnly, time.Now())
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if specRow.SpecHash == before.SpecHash {
		t.Fatal("a spec change left the spec hash alone")
	}
}

func TestProjectHashIsStable(t *testing.T) {
	kind := agentKind()
	first, err := Project(kind, testAgent(), time.Now())
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	second, err := Project(kind, testAgent(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if first.SpecHash != second.SpecHash || first.StatusHash != second.StatusHash {
		t.Fatal("the same object hashed differently; a resync would rewrite every row")
	}
	if string(first.Spec) != string(second.Spec) {
		t.Fatal("the same object marshalled differently")
	}
}

func TestRegistryMatchesTheSharedKindList(t *testing.T) {
	// The configuration validates a read backend against the shared list in
	// pkg/readmodel; a kind added to one and not the other would be configurable
	// but never projected, or projected but never configurable.
	if got, want := len(Registry()), len(KnownKinds()); got != want {
		t.Fatalf("registry has %d kinds, the shared list has %d", got, want)
	}
	for _, kind := range Registry() {
		if !IsKnownKind(kind.Kind) {
			t.Fatalf("registry kind %s is not in pkg/readmodel", kind.Kind)
		}
	}
}
