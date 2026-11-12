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
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// A disabled CustomCommand is invisible to resolution, exactly like a disabled
// Skill. The kind was missing from enabled()'s switch once, and the default
// answer treated it as reachable.
func TestResolveCustomCommandsTreatsADisabledCommandAsMissing(t *testing.T) {
	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "helper", Namespace: "default"},
		Spec: agentv1alpha1.AgentSpec{
			Enabled:        true,
			CustomCommands: []agentv1alpha1.Reference{{Name: "triage"}, {Name: "live"}},
		},
	}
	reconciler := newFakeReconciler(t,
		&agentv1alpha1.CustomCommand{
			ObjectMeta: metav1.ObjectMeta{Name: "triage", Namespace: "default"},
			Spec:       agentv1alpha1.CustomCommandSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: false}},
		},
		&agentv1alpha1.CustomCommand{
			ObjectMeta: metav1.ObjectMeta{Name: "live", Namespace: "default"},
			Spec:       agentv1alpha1.CustomCommandSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}},
		},
	)

	names, missing, err := reconciler.resolveCustomCommands(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolveCustomCommands: %v", err)
	}
	if want := []string{"live"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	if want := []string{"CustomCommand/default/triage"}; !reflect.DeepEqual(missing, want) {
		t.Errorf("missing = %v, want %v", missing, want)
	}
}

// An unknown kind contributes nothing. The alternative — treating it as
// reachable — is how a new gated resource becomes visible by omission.
func TestEnabledFailsClosedForAnUnknownKind(t *testing.T) {
	if enabled(&chatv1alpha1.Command{ObjectMeta: metav1.ObjectMeta{Name: "bind"}}) {
		t.Fatal("enabled() reported an unknown kind as enabled")
	}
	if !enabled(&agentv1alpha1.CustomCommand{
		Spec: agentv1alpha1.CustomCommandSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}},
	}) {
		t.Fatal("enabled() reported an enabled CustomCommand as disabled")
	}
}

// The selector path applies the same "disabled is invisible" rule as a named
// reference: both now go through enabled().
func TestResolveSkillsSelectorSkipsDisabled(t *testing.T) {
	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "helper", Namespace: "default"},
		Spec: agentv1alpha1.AgentSpec{
			Enabled:       true,
			SkillSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "sre"}},
		},
	}
	reconciler := newFakeReconciler(t,
		&agentv1alpha1.Skill{
			ObjectMeta: metav1.ObjectMeta{Name: "live", Namespace: "default",
				Labels: map[string]string{"team": "sre"}},
			Spec: agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}},
		},
		&agentv1alpha1.Skill{
			ObjectMeta: metav1.ObjectMeta{Name: "off", Namespace: "default",
				Labels: map[string]string{"team": "sre"}},
			Spec: agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: false}},
		},
	)

	names, missing, err := reconciler.resolveSkills(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolveSkills: %v", err)
	}
	if want := []string{"live"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none: a selector match is not a reference", missing)
	}
}

// The same modelRef is read twice: existence here, and again by the
// aggregation resolver, which honours an explicit namespace. Checking in a
// different namespace than the one that will be used makes the two disagree
// about one field — the agent reports a missing model while aggregation uses
// it happily.
func TestResolveChecksTheModelWhereAggregationUsesIt(t *testing.T) {
	model := &agentv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "other"},
		Spec: agentv1alpha1.ModelSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
			Endpoint:   "https://example.invalid/v1",
			Model:      "shared",
		},
	}
	agent := fakeAgent("root")
	agent.Spec.ModelRef = &agentv1alpha1.Reference{Name: "shared", Namespace: "other"}
	// A model in another namespace is shareable, so it needs a grant from the
	// agent's namespace. Without one the reference is reported missing even
	// though the row exists — the same rule the runtime applies.
	grant := &agentv1alpha1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-shared", Namespace: "other"},
		Spec: agentv1alpha1.ReferenceGrantSpec{
			From: []agentv1alpha1.ReferenceGrantFrom{{
				Group: sharing.GroupName, Kind: sharing.KindAgent, Namespace: "default",
			}},
			To: []agentv1alpha1.ReferenceGrantTo{{
				Group: sharing.GroupName, Kind: sharing.KindModel, Name: "shared",
			}},
		},
	}

	rec := newFakeReconciler(t, agent, model, grant)
	result, err := rec.resolve(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.Resolved.Model != "shared" {
		t.Fatalf("resolved model = %q, want the model the aggregation resolver uses", result.Resolved.Model)
	}
	if !hasCondition(result.Conditions, agentv1alpha1.ConditionRefsResolved, metav1.ConditionTrue, "AllRefsFound") {
		t.Fatalf("a model that exists where it is used was reported missing: %+v", result.Conditions)
	}
}

// A model that is absent from the namespace the resolver would use is the real
// failure, and the message has to name that namespace: the agent's own is the
// obvious wrong place to look.
func TestResolveReportsAMissingModelInTheReferencedNamespace(t *testing.T) {
	agent := fakeAgent("root")
	agent.Spec.ModelRef = &agentv1alpha1.Reference{Name: "shared", Namespace: "other"}

	rec := newFakeReconciler(t, agent)
	result, err := rec.resolve(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.Resolved.Model != "" {
		t.Fatalf("resolved model = %q, want none", result.Resolved.Model)
	}
	if !hasCondition(result.Conditions, agentv1alpha1.ConditionRefsResolved, metav1.ConditionFalse, "ReferenceNotFound") {
		t.Fatalf("missing ReferenceNotFound condition: %+v", result.Conditions)
	}
}
