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

package agentbinding

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// memoryClient is a read-only client.Client. controller-runtime's fake client
// cannot be used here: k8s v0.38 moved to structured-merge-diff v7 while the
// fake client still uses v6, and the two types do not convert.
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

func fakeCommand(name string, enabled bool) *chatv1alpha1.Command {
	return &chatv1alpha1.Command{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: chatv1alpha1.CommandSpec{
			CommonSpec: chatv1alpha1.CommonSpec{Enabled: enabled},
			Group:      "core",
		},
	}
}

func TestComputeChecksCommandsAgainstTheCatalogue(t *testing.T) {
	binding := &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "default"},
		Spec: agentv1alpha1.AgentBindingSpec{
			Agents:   []string{"helper"},
			Commands: []string{"job", "cronjob", "skill:resume-review", "gone"},
		},
	}
	c := &memoryClient{objects: []client.Object{
		fakeCommand("job", true),
		fakeCommand("cronjob", false),
	}}

	got, err := Compute(context.Background(), c, binding)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if fmt.Sprint(got.Commands) != "[job skill:resume-review]" {
		t.Errorf("Commands = %v, want the enabled catalogue entries and the skill reference", got.Commands)
	}
	if fmt.Sprint(got.Missing) != "[Command/default/cronjob Command/default/gone]" {
		t.Errorf("Missing = %v, want the disabled and unknown commands", got.Missing)
	}
}

func TestComputeExpandsTheCommandSelector(t *testing.T) {
	binding := &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "default"},
		Spec: agentv1alpha1.AgentBindingSpec{
			Agents:   []string{"helper"},
			Commands: []string{"oncall"},
			CommandSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "agent.onexstack.io/group",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"duty"},
				}},
			},
		},
	}

	oncall := fakeCommand("oncall", true)
	oncall.Labels = map[string]string{"agent.onexstack.io/group": "duty"}
	hand := fakeCommand("hand", true)
	hand.Labels = map[string]string{"agent.onexstack.io/group": "duty"}
	disabled := fakeCommand("mark", false)
	disabled.Labels = map[string]string{"agent.onexstack.io/group": "duty"}
	job := fakeCommand("job", true)
	job.Labels = map[string]string{"agent.onexstack.io/group": "job"}

	c := &memoryClient{objects: []client.Object{oncall, hand, disabled, job}}

	got, err := Compute(context.Background(), c, binding)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// oncall is both named and selected; it must appear once. mark is disabled,
	// job is in another group.
	if fmt.Sprint(got.Commands) != "[hand oncall]" {
		t.Errorf("Commands = %v, want the group expanded and deduplicated", got.Commands)
	}
	if len(got.Missing) != 0 {
		t.Errorf("Missing = %v, want none", got.Missing)
	}
}

func TestComputeWithoutCommandsSkipsTheCatalogue(t *testing.T) {
	binding := &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "default"},
		Spec:       agentv1alpha1.AgentBindingSpec{Agents: []string{"helper"}},
	}
	c := &memoryClient{}

	got, err := Compute(context.Background(), c, binding)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(got.Commands) != 0 || len(got.Missing) != 0 {
		t.Errorf("an empty command list must not read the catalogue: %+v", got)
	}
}

// One row per kind the union carries. The table exists to catch an omission:
// a kind dropped from Contribute, wired to the wrong field, or checked as the
// wrong type. Each row grants three references — present, disabled, absent —
// and exactly one may survive.
func TestComputeResolvesEveryKind(t *testing.T) {
	three := func() []agentv1alpha1.Reference {
		return []agentv1alpha1.Reference{{Name: "present"}, {Name: "disabled"}, {Name: "gone"}}
	}

	tests := []struct {
		kind  string
		set   func(*agentv1alpha1.AgentBindingSpec)
		build func(name string, enabled bool) client.Object
		field func(Contribution) []string
	}{
		{
			kind: "Prompt",
			set:  func(s *agentv1alpha1.AgentBindingSpec) { s.Prompts = three() },
			build: func(name string, enabled bool) client.Object {
				return &agentv1alpha1.Prompt{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec:       agentv1alpha1.PromptSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: enabled}},
				}
			},
			field: func(c Contribution) []string { return c.Prompts },
		},
		{
			kind: "Skill",
			set:  func(s *agentv1alpha1.AgentBindingSpec) { s.Skills = three() },
			build: func(name string, enabled bool) client.Object {
				return &agentv1alpha1.Skill{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec:       agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: enabled}},
				}
			},
			field: func(c Contribution) []string { return c.Skills },
		},
		{
			kind: "KnowledgeBase",
			set:  func(s *agentv1alpha1.AgentBindingSpec) { s.KnowledgeBases = three() },
			build: func(name string, enabled bool) client.Object {
				return &agentv1alpha1.KnowledgeBase{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec:       agentv1alpha1.KnowledgeBaseSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: enabled}},
				}
			},
			field: func(c Contribution) []string { return c.KnowledgeBases },
		},
		{
			kind: "MCPServer",
			set:  func(s *agentv1alpha1.AgentBindingSpec) { s.MCPServers = three() },
			build: func(name string, enabled bool) client.Object {
				return &agentv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec:       agentv1alpha1.MCPServerSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: enabled}},
				}
			},
			field: func(c Contribution) []string { return c.MCPs },
		},
		{
			kind: "CustomCommand",
			set:  func(s *agentv1alpha1.AgentBindingSpec) { s.CustomCommands = three() },
			build: func(name string, enabled bool) client.Object {
				return &agentv1alpha1.CustomCommand{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec: agentv1alpha1.CustomCommandSpec{
						CommonSpec: agentv1alpha1.CommonSpec{Enabled: enabled},
						Type:       "prompt",
						Prompt:     &agentv1alpha1.PromptCommandSpec{Template: "x"},
					},
				}
			},
			field: func(c Contribution) []string { return c.CustomCommands },
		},
	}

	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			spec := &agentv1alpha1.AgentBindingSpec{}
			tc.set(spec)
			binding := &agentv1alpha1.AgentBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "default"},
				Spec:       *spec,
			}
			c := &memoryClient{objects: []client.Object{
				tc.build("present", true),
				tc.build("disabled", false),
			}}

			got, err := Compute(context.Background(), c, binding)
			if err != nil {
				t.Fatalf("Compute: %v", err)
			}
			if names := fmt.Sprint(tc.field(got)); names != "[present]" {
				t.Fatalf("%s = %s, want only the enabled object", tc.kind, names)
			}
			want := fmt.Sprintf("[%s/default/disabled %s/default/gone]", tc.kind, tc.kind)
			if missing := fmt.Sprint(got.Missing); missing != want {
				t.Fatalf("Missing = %s, want %s", missing, want)
			}
		})
	}
}

// A reference that names another namespace is reported, not resolved. The union
// records bare names and every consumer resolves them in the binding's own
// namespace, so checking the object elsewhere would vouch for one object and
// grant a different one.
func TestComputeReportsACrossNamespaceReference(t *testing.T) {
	binding := &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "default"},
		Spec: agentv1alpha1.AgentBindingSpec{
			Prompts: []agentv1alpha1.Reference{{Name: "elsewhere", Namespace: "other"}},
		},
	}
	other := &agentv1alpha1.Prompt{
		ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: "other"},
		Spec:       agentv1alpha1.PromptSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}},
	}

	got, err := Compute(context.Background(), &memoryClient{objects: []client.Object{other}}, binding)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(got.Prompts) != 0 {
		t.Fatalf("Prompts = %v, want nothing granted", got.Prompts)
	}
	if fmt.Sprint(got.Missing) != "[Prompt/other/elsewhere (namespace not allowed)]" {
		t.Fatalf("Missing = %v", got.Missing)
	}
}

func TestMatchAgentsCountsEachAgentOnce(t *testing.T) {
	agent := func(name, tier string, enabled bool) *agentv1alpha1.Agent {
		a := &agentv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       agentv1alpha1.AgentSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: enabled}},
		}
		if tier != "" {
			a.Labels = map[string]string{"tier": tier}
		}
		return a
	}
	base := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "base"}}

	tests := []struct {
		name    string
		spec    agentv1alpha1.AgentBindingSpec
		objects []client.Object
		want    int32
	}{
		{
			name:    "named agents",
			spec:    agentv1alpha1.AgentBindingSpec{Agents: []string{"a", "b"}},
			objects: []client.Object{agent("a", "", true), agent("b", "", true), agent("c", "", true)},
			want:    2,
		},
		{
			name:    "selector matches",
			spec:    agentv1alpha1.AgentBindingSpec{Selector: base},
			objects: []client.Object{agent("a", "base", true), agent("b", "base", true), agent("c", "edge", true)},
			want:    2,
		},
		{
			name:    "an agent named and matched counts once",
			spec:    agentv1alpha1.AgentBindingSpec{Agents: []string{"a"}, Selector: base},
			objects: []client.Object{agent("a", "base", true), agent("b", "base", true)},
			want:    2,
		},
		{
			name:    "a disabled agent is not a match, named or selected",
			spec:    agentv1alpha1.AgentBindingSpec{Agents: []string{"a"}, Selector: base},
			objects: []client.Object{agent("a", "base", false), agent("b", "base", true)},
			want:    1,
		},
		{
			name: "another namespace is not counted",
			spec: agentv1alpha1.AgentBindingSpec{Agents: []string{"a"}},
			objects: []client.Object{
				agent("a", "", true),
				&agentv1alpha1.Agent{
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "other"},
					Spec:       agentv1alpha1.AgentSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}},
				},
			},
			want: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binding := &agentv1alpha1.AgentBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "default"},
				Spec:       tc.spec,
			}
			got, err := MatchAgents(context.Background(), &memoryClient{objects: tc.objects}, binding)
			if err != nil {
				t.Fatalf("MatchAgents: %v", err)
			}
			if got != tc.want {
				t.Fatalf("matched = %d, want %d", got, tc.want)
			}
		})
	}
}

// Only an explicit empty object selects everything. A missing selector selects
// nothing, which is a different statement and must not read as "all agents".
func TestSelectorMatchesAll(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selector *metav1.LabelSelector
		want     bool
	}{
		{"absent", nil, false},
		{"empty", &metav1.LabelSelector{}, true},
		{"labelled", &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "base"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := &agentv1alpha1.AgentBinding{
				Spec: agentv1alpha1.AgentBindingSpec{Selector: tc.selector},
			}
			if got := SelectorMatchesAll(binding); got != tc.want {
				t.Fatalf("SelectorMatchesAll = %v, want %v", got, tc.want)
			}
		})
	}
}

// The switch has no case for most kinds, and an unmatched kind must contribute
// nothing. Defaulting to true would make every future kind silently reachable
// through a binding written before that kind existed.
func TestEnabledFailsClosed(t *testing.T) {
	if enabled(&chatv1alpha1.Command{
		Spec: chatv1alpha1.CommandSpec{CommonSpec: chatv1alpha1.CommonSpec{Enabled: true}},
	}) {
		t.Fatalf("a kind without a case must not count as enabled")
	}
}
