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

package agent

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func newFakeReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := agentv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return &Reconciler{Client: &memoryClient{objects: objs}, Scheme: scheme}
}

// memoryClient is a read-only client.Client. controller-runtime's fake client
// cannot be used here: k8s v0.38 moved to structured-merge-diff v7 while the
// fake client still uses v6, and the two types do not convert.
//
// The embedded interface is deliberately nil: resolution only reads, and a
// write reaching a test means the test is exercising the wrong path.
type memoryClient struct {
	client.Client
	objects []client.Object
}

func (c *memoryClient) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	for _, object := range c.objects {
		if reflect.TypeOf(object) != reflect.TypeOf(obj) {
			continue
		}
		if object.GetNamespace() != key.Namespace || object.GetName() != key.Name {
			continue
		}
		reflect.ValueOf(obj).Elem().Set(reflect.ValueOf(object).Elem())
		return nil
	}
	return apierrors.NewNotFound(schema.GroupResource{}, key.Name)
}

func (c *memoryClient) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	var options client.ListOptions
	options.ApplyOptions(opts)

	items := reflect.ValueOf(list).Elem().FieldByName("Items")
	itemType := items.Type().Elem()

	for _, object := range c.objects {
		value := reflect.ValueOf(object)
		if value.Type() != reflect.PointerTo(itemType) {
			continue
		}
		if options.Namespace != "" && object.GetNamespace() != options.Namespace {
			continue
		}
		if options.LabelSelector != nil && !options.LabelSelector.Matches(labels.Set(object.GetLabels())) {
			continue
		}
		items.Set(reflect.Append(items, value.Elem()))
	}
	return nil
}

// fakeAgent builds an enabled agent in the default namespace.
func fakeAgent(name string) *agentv1alpha1.Agent {
	return &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: agentv1alpha1.AgentSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
		},
	}
}

func withAgents(agent *agentv1alpha1.Agent, names ...string) *agentv1alpha1.Agent {
	for _, name := range names {
		agent.Spec.Agents = append(agent.Spec.Agents, agentv1alpha1.Reference{Name: name})
	}
	return agent
}

func sourceNames(set agentSet) []string {
	names := make([]string, 0, len(set.sources))
	for _, source := range set.sources {
		names = append(names, source.Ref.Name)
	}
	return names
}

func TestResolveAgentSourcesByName(t *testing.T) {
	root := withAgents(fakeAgent("root"), "base-a", "base-b")
	rec := newFakeReconciler(t, root, fakeAgent("base-a"), fakeAgent("base-b"))

	set, err := rec.resolveAgentSources(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve sources: %v", err)
	}
	if !set.usable() {
		t.Fatalf("set is not usable: %+v", set)
	}
	if got := fmt.Sprint(sourceNames(set)); got != "[base-a base-b]" {
		t.Fatalf("sources = %s, want declaration order", got)
	}
}

func TestResolveAgentSourcesBySelector(t *testing.T) {
	root := fakeAgent("root")
	root.Spec.AgentSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "base"}}

	labelled := func(name string) *agentv1alpha1.Agent {
		agent := fakeAgent(name)
		agent.Labels = map[string]string{"tier": "base"}
		return agent
	}
	rec := newFakeReconciler(t, root, labelled("b-agent"), labelled("a-agent"), fakeAgent("unrelated"))

	set, err := rec.resolveAgentSources(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve sources: %v", err)
	}
	if got := fmt.Sprint(sourceNames(set)); got != "[a-agent b-agent]" {
		t.Fatalf("sources = %s, want selector matches sorted", got)
	}
}

func TestResolveAgentSourcesNeverSelectsItself(t *testing.T) {
	root := fakeAgent("root")
	root.Labels = map[string]string{"tier": "base"}
	root.Spec.AgentSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "base"}}
	rec := newFakeReconciler(t, root)

	set, err := rec.resolveAgentSources(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve sources: %v", err)
	}
	if len(set.sources) != 0 || set.cycle {
		t.Fatalf("an agent must not aggregate itself: %+v", set)
	}
}

func TestResolveAgentSourcesTransitive(t *testing.T) {
	root := withAgents(fakeAgent("root"), "mid")
	mid := withAgents(fakeAgent("mid"), "base")
	rec := newFakeReconciler(t, root, mid, fakeAgent("base"))

	set, err := rec.resolveAgentSources(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve sources: %v", err)
	}
	if got := fmt.Sprint(sourceNames(set)); got != "[mid base]" {
		t.Fatalf("sources = %s, want the transitive closure", got)
	}
}

func TestResolveAgentSourcesDiamondIsDeduped(t *testing.T) {
	root := withAgents(fakeAgent("root"), "left", "right")
	left := withAgents(fakeAgent("left"), "base")
	right := withAgents(fakeAgent("right"), "base")
	rec := newFakeReconciler(t, root, left, right, fakeAgent("base"))

	set, err := rec.resolveAgentSources(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve sources: %v", err)
	}
	if got := fmt.Sprint(sourceNames(set)); got != "[left right base]" {
		t.Fatalf("sources = %s, want base once", got)
	}
	if set.cycle {
		t.Fatalf("a diamond is not a cycle")
	}
}

func TestResolveAgentSourcesCycleFailsClosed(t *testing.T) {
	root := withAgents(fakeAgent("root"), "a")
	a := withAgents(fakeAgent("a"), "b")
	b := withAgents(fakeAgent("b"), "a")
	rec := newFakeReconciler(t, root, a, b)

	set, err := rec.resolveAgentSources(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve sources: %v", err)
	}
	if !set.cycle {
		t.Fatalf("expected a cycle, got %+v", set)
	}
	if set.usable() {
		t.Fatalf("a cycle must make the union unusable")
	}
}

func TestResolveAgentSourcesDepthExceeded(t *testing.T) {
	// A chain deeper than maxAgentAggregationDepth: root -> a0 -> a1 -> ...
	// The walk must stop and mark the union unusable rather than publish a
	// truncated one.
	root := withAgents(fakeAgent("root"), "a0")
	objs := []client.Object{root}
	for i := 0; i <= maxAgentAggregationDepth+1; i++ {
		agent := fakeAgent(fmt.Sprintf("a%d", i))
		if i < maxAgentAggregationDepth+1 {
			agent.Spec.Agents = []agentv1alpha1.Reference{{Name: fmt.Sprintf("a%d", i+1)}}
		}
		objs = append(objs, agent)
	}

	rec := newFakeReconciler(t, objs...)
	set, err := rec.resolveAgentSources(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve sources: %v", err)
	}
	if !set.tooDeep {
		t.Fatalf("expected tooDeep, got %+v", set)
	}
	if set.usable() {
		t.Fatalf("a truncated closure must not be usable")
	}
}

func TestResolveAgentSourcesTooMany(t *testing.T) {
	root := fakeAgent("root")
	root.Spec.AgentSelector = &metav1.LabelSelector{}
	objs := []client.Object{root}
	for i := 0; i <= maxAgentSources; i++ {
		agent := fakeAgent(fmt.Sprintf("a%02d", i))
		agent.Labels = map[string]string{"base": "true"}
		agent.Spec.Enabled = true
		objs = append(objs, agent)
	}

	rec := newFakeReconciler(t, objs...)
	set, err := rec.resolveAgentSources(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve sources: %v", err)
	}
	if !set.tooMany {
		t.Fatalf("expected tooMany, got %+v", set)
	}
	if set.usable() {
		t.Fatalf("an over-cap union must not be usable")
	}
}

func TestResolveAgentSourcesCrossNamespaceIsMissing(t *testing.T) {
	root := fakeAgent("root")
	root.Spec.Agents = []agentv1alpha1.Reference{{Name: "elsewhere", Namespace: "other"}}
	other := fakeAgent("elsewhere")
	other.Namespace = "other"

	rec := newFakeReconciler(t, root, other)
	set, err := rec.resolveAgentSources(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve sources: %v", err)
	}
	if len(set.sources) != 0 {
		t.Fatalf("a cross-namespace source must not be merged: %+v", set.sources)
	}
	if len(set.missing) != 1 {
		t.Fatalf("missing = %v, want the cross-namespace reference", set.missing)
	}
}

func TestResolveAgentSourcesMissingAndDisabled(t *testing.T) {
	root := withAgents(fakeAgent("root"), "missing", "disabled")
	disabled := fakeAgent("disabled")
	disabled.Spec.Enabled = false
	rec := newFakeReconciler(t, root, disabled)

	set, err := rec.resolveAgentSources(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve sources: %v", err)
	}
	if len(set.missing) != 2 {
		t.Fatalf("missing = %v, want both entries", set.missing)
	}
	if !set.usable() {
		t.Fatalf("an unavailable source is reported, not structural")
	}
}

func TestResolveMergesInheritedCapabilities(t *testing.T) {
	root := withAgents(fakeAgent("root"), "base")
	root.Spec.Skills = []agentv1alpha1.Reference{{Name: "root-skill"}}
	root.Spec.RequireApproval = []string{"root_*"}
	root.Spec.Webhooks = []string{"root-hook"}

	base := fakeAgent("base")
	base.Spec.Skills = []agentv1alpha1.Reference{{Name: "base-skill"}}
	base.Spec.MCPs = []agentv1alpha1.Reference{{Name: "base-mcp"}}
	base.Spec.KnowledgeBases = []agentv1alpha1.Reference{{Name: "base-kb"}}
	base.Spec.RequireApproval = []string{"base_*"}
	base.Spec.Webhooks = []string{"base-hook"}
	base.Spec.Prompts = []agentv1alpha1.Reference{{Name: "base-prompt"}}

	objs := []client.Object{
		root, base,
		&agentv1alpha1.Skill{ObjectMeta: metav1.ObjectMeta{Name: "root-skill", Namespace: "default"}, Spec: agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Skill{ObjectMeta: metav1.ObjectMeta{Name: "base-skill", Namespace: "default"}, Spec: agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "base-mcp", Namespace: "default"}, Spec: agentv1alpha1.MCPServerSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.KnowledgeBase{ObjectMeta: metav1.ObjectMeta{Name: "base-kb", Namespace: "default"}, Spec: agentv1alpha1.KnowledgeBaseSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Webhook{ObjectMeta: metav1.ObjectMeta{Name: "root-hook", Namespace: "default"}, Spec: agentv1alpha1.WebhookSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Webhook{ObjectMeta: metav1.ObjectMeta{Name: "base-hook", Namespace: "default"}, Spec: agentv1alpha1.WebhookSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Prompt{ObjectMeta: metav1.ObjectMeta{Name: "base-prompt", Namespace: "default"}, Spec: agentv1alpha1.PromptSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}, Template: "base persona"}},
	}

	rec := newFakeReconciler(t, objs...)
	result, err := rec.resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	assertStrings(t, "skills", result.Resolved.Skills, "base-skill", "root-skill")
	assertStrings(t, "mcps", result.Resolved.MCPs, "base-mcp")
	assertStrings(t, "knowledgeBases", result.Resolved.KnowledgeBases, "base-kb")
	assertStrings(t, "webhooks", result.Resolved.Webhooks, "base-hook", "root-hook")
	assertStrings(t, "requireApproval", result.Resolved.RequireApproval, "base_*", "root_*")
	assertStrings(t, "agents", result.Resolved.Agents, "base")

	if got := fmt.Sprint(result.Prompts.availableNames()); got != "[base-prompt]" {
		t.Fatalf("prompt sources = %s, want the source agent's prompt", got)
	}
}

func TestResolveCyclePublishesOnlyRootCapabilities(t *testing.T) {
	root := withAgents(fakeAgent("root"), "a")
	a := withAgents(fakeAgent("a"), "root")
	a.Spec.Skills = []agentv1alpha1.Reference{{Name: "a-skill"}}
	root.Spec.Skills = []agentv1alpha1.Reference{{Name: "root-skill"}}

	objs := []client.Object{
		root, a,
		&agentv1alpha1.Skill{ObjectMeta: metav1.ObjectMeta{Name: "root-skill", Namespace: "default"}, Spec: agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		&agentv1alpha1.Skill{ObjectMeta: metav1.ObjectMeta{Name: "a-skill", Namespace: "default"}, Spec: agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
	}

	rec := newFakeReconciler(t, objs...)
	result, err := rec.resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assertStrings(t, "skills", result.Resolved.Skills, "root-skill")
	assertStrings(t, "agents", result.Resolved.Agents)
	if !hasCondition(result.Conditions, agentv1alpha1.ConditionAgentAggregated, metav1.ConditionFalse, "CycleDetected") {
		t.Fatalf("missing CycleDetected condition: %+v", result.Conditions)
	}
	if ready := readyCondition(result.Conditions, root.Generation); ready.Status != metav1.ConditionFalse {
		t.Fatalf("a cycle must make the agent not Ready: %+v", ready)
	}
}

func TestResolveDoesNotInheritModelOrAuthzBackends(t *testing.T) {
	root := withAgents(fakeAgent("root"), "base")
	base := fakeAgent("base")
	base.Spec.ModelRef = &agentv1alpha1.Reference{Name: "base-model"}
	base.Spec.AuthzBackends = []string{"none"}

	rec := newFakeReconciler(t, root, base)
	result, err := rec.resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.Resolved.Model != "" {
		t.Fatalf("model = %q, want no inheritance", result.Resolved.Model)
	}
}

func TestResolveMissingSourceKeepsLastGoodPrompt(t *testing.T) {
	root := withAgents(fakeAgent("root"), "gone")
	rec := newFakeReconciler(t, root)

	result, err := rec.resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !result.Prompts.sourceUnavailable {
		t.Fatalf("a missing source must mark the prompt set unavailable")
	}
	if !hasCondition(result.Conditions, agentv1alpha1.ConditionAgentAggregated, metav1.ConditionFalse, "SourceUnavailable") {
		t.Fatalf("missing SourceUnavailable condition: %+v", result.Conditions)
	}
}

func TestResolveInheritsSourceSelectorMatches(t *testing.T) {
	root := withAgents(fakeAgent("root"), "base")
	base := fakeAgent("base")
	base.Spec.SkillSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"shared": "true"}}

	selected := &agentv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "selected-skill", Namespace: "default", Labels: map[string]string{"shared": "true"}},
		Spec:       agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}},
	}
	disabled := &agentv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "disabled-skill", Namespace: "default", Labels: map[string]string{"shared": "true"}},
		Spec:       agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: false}},
	}
	unrelated := &agentv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "other-skill", Namespace: "default"},
		Spec:       agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}},
	}

	rec := newFakeReconciler(t, root, base, selected, disabled, unrelated)
	result, err := rec.resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assertStrings(t, "skills", result.Resolved.Skills, "selected-skill")
}

func TestAggregatedCondition(t *testing.T) {
	agent := fakeAgent("root")

	if condition := aggregatedCondition(agent, agentSet{}); condition.Status != metav1.ConditionTrue || condition.Reason != "NoSources" {
		t.Fatalf("no sources = %+v, want NoSources/True", condition)
	}
	one := agentSet{sources: []agentSource{{Ref: agentRef{Name: "base"}}}}
	if condition := aggregatedCondition(agent, one); condition.Status != metav1.ConditionTrue || condition.Reason != "Aggregated" {
		t.Fatalf("one source = %+v, want Aggregated/True", condition)
	}
	for _, tc := range []struct {
		set    agentSet
		reason string
	}{
		{agentSet{cycle: true}, "CycleDetected"},
		{agentSet{tooDeep: true}, "DepthExceeded"},
		{agentSet{tooMany: true}, "TooManySources"},
		// The two caps are separate conditions: one is about how many agents
		// are aggregated, the other about how many prompts they bring. Saying
		// "too many sources" for the second sends an operator to the wrong list.
		{agentSet{tooManyPrompts: true}, "TooManyPromptSources"},
		{agentSet{missing: []string{"Agent/gone"}}, "SourceUnavailable"},
	} {
		condition := aggregatedCondition(agent, tc.set)
		if condition.Status != metav1.ConditionFalse || condition.Reason != tc.reason {
			t.Fatalf("set %+v = %+v, want %s/False", tc.set, condition, tc.reason)
		}
		if ready := readyCondition([]metav1.Condition{condition}, agent.Generation); ready.Status != metav1.ConditionFalse {
			t.Fatalf("%s must make the agent not Ready", tc.reason)
		}
	}
}

func TestMergePromptSetDedupes(t *testing.T) {
	base := promptSet{
		declared: []promptRef{{Namespace: "default", Name: "shared"}, {Namespace: "default", Name: "base"}},
		available: []promptSource{
			{Namespace: "default", Name: "shared", Template: "base-shared"},
			{Namespace: "default", Name: "base", Template: "base-only"},
		},
	}
	merged := promptSet{
		declared: []promptRef{{Namespace: "default", Name: "shared"}, {Namespace: "default", Name: "extra"}},
		available: []promptSource{
			{Namespace: "default", Name: "shared", Template: "source-shared"},
			{Namespace: "default", Name: "extra", Template: "source-only"},
		},
	}

	mergePromptSet(&base, merged)

	if got := fmt.Sprint(base.availableNames()); got != "[shared base extra]" {
		t.Fatalf("available = %s, want deduped in order", got)
	}
	if len(base.available) != 3 {
		t.Fatalf("available = %d entries, want 3", len(base.available))
	}
	// The root's content wins for a name both declare.
	if base.available[0].Template != "base-shared" {
		t.Fatalf("the root's source must not be replaced: %q", base.available[0].Template)
	}
}

func TestMergePromptSetTooMany(t *testing.T) {
	dst := promptSet{}
	for i := 0; i < maxPromptSources; i++ {
		dst.declared = append(dst.declared, promptRef{Namespace: "default", Name: fmt.Sprintf("p%d", i)})
	}
	src := promptSet{declared: []promptRef{{Namespace: "default", Name: "one-too-many"}}}

	mergePromptSet(&dst, src)
	if !dst.tooMany {
		t.Fatalf("merging past the cap must set tooMany")
	}
}

func assertStrings(t *testing.T, field string, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s = %v, want %v", field, got, want)
	}
}

func hasCondition(conditions []metav1.Condition, kind string, status metav1.ConditionStatus, reason string) bool {
	for _, condition := range conditions {
		if condition.Type == kind && condition.Status == status && condition.Reason == reason {
			return true
		}
	}
	return false
}

func fakeSkill(name string) *agentv1alpha1.Skill {
	return &agentv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}},
	}
}

// A structural failure makes the whole union unusable — not a prefix of it.
// The reason matters as much as the outcome: each one points at a different
// object to go and fix.
func TestResolveStructuralFailureInheritsNothing(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		build  func() (*agentv1alpha1.Agent, []client.Object)
	}{
		{
			name:   "cycle",
			reason: "CycleDetected",
			build: func() (*agentv1alpha1.Agent, []client.Object) {
				root := withAgents(fakeAgent("root"), "a")
				a := withAgents(fakeAgent("a"), "root")
				a.Spec.Skills = []agentv1alpha1.Reference{{Name: "base-skill"}}
				return root, []client.Object{root, a, fakeSkill("base-skill")}
			},
		},
		{
			name:   "too deep",
			reason: "DepthExceeded",
			build: func() (*agentv1alpha1.Agent, []client.Object) {
				root := withAgents(fakeAgent("root"), "a0")
				objs := []client.Object{root}
				for i := 0; i <= maxAgentAggregationDepth+1; i++ {
					agent := fakeAgent(fmt.Sprintf("a%d", i))
					if i < maxAgentAggregationDepth+1 {
						agent.Spec.Agents = []agentv1alpha1.Reference{{Name: fmt.Sprintf("a%d", i+1)}}
					}
					agent.Spec.Skills = []agentv1alpha1.Reference{{Name: "base-skill"}}
					objs = append(objs, agent)
				}
				return root, append(objs, fakeSkill("base-skill"))
			},
		},
		{
			name:   "too many agents",
			reason: "TooManySources",
			build: func() (*agentv1alpha1.Agent, []client.Object) {
				root := fakeAgent("root")
				root.Spec.AgentSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "base"}}
				objs := []client.Object{root, fakeSkill("base-skill")}
				for i := 0; i <= maxAgentSources; i++ {
					agent := fakeAgent(fmt.Sprintf("a%02d", i))
					agent.Labels = map[string]string{"tier": "base"}
					agent.Spec.Skills = []agentv1alpha1.Reference{{Name: "base-skill"}}
					objs = append(objs, agent)
				}
				return root, objs
			},
		},
		{
			name:   "too many prompt sources",
			reason: "TooManyPromptSources",
			build: func() (*agentv1alpha1.Agent, []client.Object) {
				// Two sources whose selectors each match more than half the cap:
				// neither is over on its own, the union is.
				root := withAgents(fakeAgent("root"), "base1", "base2")
				objs := []client.Object{root, fakeSkill("base-skill")}
				for _, group := range []string{"base1", "base2"} {
					base := fakeAgent(group)
					base.Spec.Skills = []agentv1alpha1.Reference{{Name: "base-skill"}}
					base.Spec.PromptSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"group": group}}
					objs = append(objs, base)
					for i := 0; i < maxPromptSources/2+4; i++ {
						objs = append(objs, &agentv1alpha1.Prompt{
							ObjectMeta: metav1.ObjectMeta{
								Name:      fmt.Sprintf("%s-p%02d", group, i),
								Namespace: "default",
								Labels:    map[string]string{"group": group},
							},
							Spec: agentv1alpha1.PromptSpec{
								CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
								Template:   "x",
							},
						})
					}
				}
				return root, objs
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, objs := tc.build()
			root.Spec.Skills = []agentv1alpha1.Reference{{Name: "root-skill"}}
			objs = append(objs, fakeSkill("root-skill"))

			rec := newFakeReconciler(t, objs...)
			result, err := rec.resolve(context.Background(), root)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}

			// The agent's own capabilities still apply; every inherited one is
			// dropped, including the ones that resolved before the failure.
			assertStrings(t, "skills", result.Resolved.Skills, "root-skill")
			assertStrings(t, "agents", result.Resolved.Agents)

			// The derived prompt keeps its last good value. Recomputing it from
			// the root alone would silently replace one that was published.
			if !result.Prompts.sourceUnavailable {
				t.Fatalf("%s must mark the derived prompt unavailable", tc.reason)
			}

			if !hasCondition(result.Conditions, agentv1alpha1.ConditionAgentAggregated, metav1.ConditionFalse, tc.reason) {
				t.Fatalf("missing %s condition: %+v", tc.reason, result.Conditions)
			}
			if ready := readyCondition(result.Conditions, root.Generation); ready.Status != metav1.ConditionFalse {
				t.Fatalf("%s must make the agent not Ready: %+v", tc.reason, ready)
			}
		})
	}
}

// A source that is merely missing is reported, not structural: the rest of the
// union still applies. Keeping this apart from the table above is the whole
// point — the two answers differ, and an operator reads a different message.
func TestResolveMissingSourceStillAppliesTheOtherSources(t *testing.T) {
	root := withAgents(fakeAgent("root"), "good", "gone")
	root.Spec.Skills = []agentv1alpha1.Reference{{Name: "root-skill"}}
	good := fakeAgent("good")
	good.Spec.Skills = []agentv1alpha1.Reference{{Name: "good-skill"}}

	rec := newFakeReconciler(t, root, good, fakeSkill("root-skill"), fakeSkill("good-skill"))
	result, err := rec.resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	assertStrings(t, "skills", result.Resolved.Skills, "good-skill", "root-skill")
	assertStrings(t, "agents", result.Resolved.Agents, "good")
	if !hasCondition(result.Conditions, agentv1alpha1.ConditionAgentAggregated, metav1.ConditionFalse, "SourceUnavailable") {
		t.Fatalf("missing SourceUnavailable condition: %+v", result.Conditions)
	}
	if !result.Prompts.sourceUnavailable {
		t.Fatalf("a missing source must keep the derived prompt's last good value")
	}
}

// declaredAgents fixes the order the union is built in, and the asymmetry
// between a named ref and a selector match: a name is a statement about one
// object, so a disabled one is reported; a selector is a query, so a disabled
// match is simply not a match.
func TestDeclaredAgentsOrderAndScope(t *testing.T) {
	labelled := func(name string, labels map[string]string, enabled bool) *agentv1alpha1.Agent {
		agent := fakeAgent(name)
		agent.Labels = labels
		agent.Spec.Enabled = enabled
		return agent
	}
	all := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "base"}}

	tests := []struct {
		name    string
		agent   *agentv1alpha1.Agent
		objects []client.Object
		want    string
	}{
		{
			name:  "declaration order with duplicates removed",
			agent: withAgents(fakeAgent("root"), "b", "a", "b"),
			want:  "[default/b default/a]",
		},
		{
			name: "named refs come before selector matches, which are sorted",
			agent: func() *agentv1alpha1.Agent {
				root := withAgents(fakeAgent("root"), "z")
				root.Spec.AgentSelector = all
				return root
			}(),
			objects: []client.Object{labelled("m", map[string]string{"tier": "base"}, true), labelled("a", map[string]string{"tier": "base"}, true)},
			want:    "[default/z default/a default/m]",
		},
		{
			name: "a name in both lists appears once",
			agent: func() *agentv1alpha1.Agent {
				root := withAgents(fakeAgent("root"), "a")
				root.Spec.AgentSelector = all
				return root
			}(),
			objects: []client.Object{labelled("a", map[string]string{"tier": "base"}, true)},
			want:    "[default/a]",
		},
		{
			name: "a disabled selector match is skipped, a disabled named ref is kept",
			agent: func() *agentv1alpha1.Agent {
				root := withAgents(fakeAgent("root"), "named-off")
				root.Spec.AgentSelector = all
				return root
			}(),
			objects: []client.Object{
				// Named and disabled: kept, so resolveAgentSources can report it.
				labelled("named-off", map[string]string{"tier": "base"}, false),
				// Only matched by the selector, and disabled: not a match.
				labelled("sel-off", map[string]string{"tier": "base"}, false),
				labelled("on", map[string]string{"tier": "base"}, true),
			},
			want: "[default/named-off default/on]",
		},
		{
			name: "a cross-namespace ref keeps its namespace",
			agent: func() *agentv1alpha1.Agent {
				root := fakeAgent("root")
				root.Spec.Agents = []agentv1alpha1.Reference{{Name: "elsewhere", Namespace: "other"}}
				return root
			}(),
			want: "[other/elsewhere]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := newFakeReconciler(t, append(tc.objects, tc.agent)...)
			refs, err := rec.declaredAgents(context.Background(), tc.agent)
			if err != nil {
				t.Fatalf("declared agents: %v", err)
			}
			rendered := make([]string, 0, len(refs))
			for _, ref := range refs {
				rendered = append(rendered, ref.Namespace+"/"+ref.Name)
			}
			if got := fmt.Sprint(rendered); got != tc.want {
				t.Fatalf("refs = %s, want %s", got, tc.want)
			}
		})
	}
}

// Naming yourself explicitly is a cycle; a selector never selects its owner.
// The two spellings differ because only one of them is a statement of intent —
// an empty selector would otherwise make every agent aggregate itself.
func TestResolveAgentSourcesSelfReferenceIsACycle(t *testing.T) {
	root := withAgents(fakeAgent("root"), "root")
	rec := newFakeReconciler(t, root)

	set, err := rec.resolveAgentSources(context.Background(), root)
	if err != nil {
		t.Fatalf("resolve sources: %v", err)
	}
	if !set.cycle || set.usable() {
		t.Fatalf("an explicit self-reference must be a cycle: %+v", set)
	}
}
