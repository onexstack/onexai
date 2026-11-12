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

package agentreference

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	versionedfake "github.com/onexstack/onexai/pkg/generated/clientset/versioned/fake"
)

const testNamespace = "default"

func deleteAttributes(t *testing.T, resource, name string) admission.Attributes {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: agentv1alpha1.GroupName, Version: "v1alpha1", Resource: resource}
	obj := &agentv1alpha1.Prompt{}
	obj.Name = name
	obj.Namespace = testNamespace
	return admission.NewAttributesRecord(
		obj, nil,
		schema.GroupVersionKind{Group: agentv1alpha1.GroupName, Version: "v1alpha1", Kind: "Prompt"},
		testNamespace, name, gvr, "",
		admission.Delete, nil, false, nil,
	)
}

func newPlugin(t *testing.T, agents ...*agentv1alpha1.Agent) *Plugin {
	t.Helper()

	objs := make([]runtime.Object, 0, len(agents))
	for _, a := range agents {
		objs = append(objs, a)
	}
	onex := versionedfake.NewSimpleClientset(objs...)
	kube := kubefake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}})

	p := New()
	p.SetOnexClientSet(onex)
	p.SetKubeClientSet(kube)
	return p
}

func agentNamed(name string, enabled bool, mutate func(*agentv1alpha1.AgentSpec)) *agentv1alpha1.Agent {
	a := &agentv1alpha1.Agent{}
	a.Name = name
	a.Namespace = testNamespace
	a.Spec.Enabled = enabled
	if mutate != nil {
		mutate(&a.Spec)
	}
	return a
}

func TestDeletingAReferencedPromptIsForbidden(t *testing.T) {
	p := newPlugin(t, agentNamed("bot", true, func(s *agentv1alpha1.AgentSpec) {
		s.PromptRef = &agentv1alpha1.Reference{Name: "greeting"}
	}))

	err := p.Validate(context.Background(), deleteAttributes(t, "prompts", "greeting"), nil)
	if err == nil {
		t.Fatal("expected the delete to be refused")
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("expected a Forbidden error, got %v", err)
	}
	if !strings.Contains(err.Error(), testNamespace+"/bot") {
		t.Errorf("the error must name the blocking agent, got %q", err.Error())
	}
}

func TestADisabledAgentDoesNotBlockDeletion(t *testing.T) {
	p := newPlugin(t, agentNamed("bot", false, func(s *agentv1alpha1.AgentSpec) {
		s.PromptRef = &agentv1alpha1.Reference{Name: "greeting"}
	}))

	if err := p.Validate(context.Background(), deleteAttributes(t, "prompts", "greeting"), nil); err != nil {
		t.Fatalf("a disabled agent should not block a delete, got %v", err)
	}
}

func TestAnUnrelatedDeleteIsAllowed(t *testing.T) {
	p := newPlugin(t, agentNamed("bot", true, func(s *agentv1alpha1.AgentSpec) {
		s.PromptRef = &agentv1alpha1.Reference{Name: "greeting"}
	}))

	if err := p.Validate(context.Background(), deleteAttributes(t, "prompts", "other"), nil); err != nil {
		t.Fatalf("expected the delete to be allowed, got %v", err)
	}
}

// spec.skills and friends hold a plain list, while promptRef is a single
// pointer; both shapes must be protected.
func TestEveryReferenceShapeIsProtected(t *testing.T) {
	cases := []struct {
		resource string
		mutate   func(*agentv1alpha1.AgentSpec)
	}{
		{"prompts", func(s *agentv1alpha1.AgentSpec) { s.Prompts = []agentv1alpha1.Reference{{Name: "target"}} }},
		{"models", func(s *agentv1alpha1.AgentSpec) { s.ModelRef = &agentv1alpha1.Reference{Name: "target"} }},
		{"skills", func(s *agentv1alpha1.AgentSpec) { s.Skills = []agentv1alpha1.Reference{{Name: "target"}} }},
		{"mcpservers", func(s *agentv1alpha1.AgentSpec) { s.MCPs = []agentv1alpha1.Reference{{Name: "target"}} }},
		{"knowledgebases", func(s *agentv1alpha1.AgentSpec) { s.KnowledgeBases = []agentv1alpha1.Reference{{Name: "target"}} }},
		{"customcommands", func(s *agentv1alpha1.AgentSpec) { s.CustomCommands = []agentv1alpha1.Reference{{Name: "target"}} }},
		{"agents", func(s *agentv1alpha1.AgentSpec) { s.Agents = []agentv1alpha1.Reference{{Name: "target"}} }},
		{"webhooks", func(s *agentv1alpha1.AgentSpec) { s.Webhooks = []string{"target"} }},
	}

	for _, tc := range cases {
		t.Run(tc.resource, func(t *testing.T) {
			p := newPlugin(t, agentNamed("bot", true, tc.mutate))
			if err := p.Validate(context.Background(), deleteAttributes(t, tc.resource, "target"), nil); err == nil {
				t.Errorf("deleting a referenced %s must be refused", tc.resource)
			}
		})
	}
}

// A reference may name another namespace explicitly; it still points here.
func TestACrossNamespaceReferenceBlocks(t *testing.T) {
	p := newPlugin(t, agentNamed("bot", true, func(s *agentv1alpha1.AgentSpec) {
		s.PromptRef = &agentv1alpha1.Reference{Name: "greeting", Namespace: testNamespace}
	}))

	if err := p.Validate(context.Background(), deleteAttributes(t, "prompts", "greeting"), nil); err == nil {
		t.Fatal("expected the delete to be refused")
	}
}

// Namespace teardown deletes everything, including objects an Agent names;
// refusing there would leave the namespace stuck in Terminating.
func TestATerminatingNamespaceIsNotBlocked(t *testing.T) {
	now := metav1.NewTime(time.Now())
	onex := versionedfake.NewSimpleClientset(agentNamed("bot", true, func(s *agentv1alpha1.AgentSpec) {
		s.PromptRef = &agentv1alpha1.Reference{Name: "greeting"}
	}))
	kube := kubefake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: testNamespace, DeletionTimestamp: &now,
	}})

	p := New()
	p.SetOnexClientSet(onex)
	p.SetKubeClientSet(kube)

	if err := p.Validate(context.Background(), deleteAttributes(t, "prompts", "greeting"), nil); err != nil {
		t.Fatalf("a terminating namespace must not be blocked, got %v", err)
	}
}

// The plugin is a safety net, not a source of truth: an unreadable agent list
// must not wedge every delete.
func TestAnUnreadableAgentListFailsOpen(t *testing.T) {
	onex := versionedfake.NewSimpleClientset()
	onex.PrependReactor("list", "agents", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(context.DeadlineExceeded)
	})
	kube := kubefake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}})

	p := New()
	p.SetOnexClientSet(onex)
	p.SetKubeClientSet(kube)

	if err := p.Validate(context.Background(), deleteAttributes(t, "prompts", "greeting"), nil); err != nil {
		t.Fatalf("expected the delete to proceed, got %v", err)
	}
}

// Resources outside the protected set are never touched.
func TestUnprotectedResourcesAreIgnored(t *testing.T) {
	p := newPlugin(t)
	attrs := deleteAttributes(t, "botapps", "anything")
	if err := p.Validate(context.Background(), attrs, nil); err != nil {
		t.Fatalf("expected a non-agent resource to be allowed, got %v", err)
	}
}

// spec.skills may name another namespace, so an agent in a different namespace
// can hold the reference. The lookup must not be namespace-scoped.
func TestACrossNamespaceAgentBlocksTheDelete(t *testing.T) {
	agent := agentNamed("bot", true, func(s *agentv1alpha1.AgentSpec) {
		s.Skills = []agentv1alpha1.Reference{{Name: "lint", Namespace: testNamespace}}
	})
	agent.Namespace = "other"

	p := newPlugin(t, agent)

	err := p.Validate(context.Background(), deleteAttributes(t, "skills", "lint"), nil)
	if err == nil {
		t.Fatal("an agent in another namespace still holds the reference")
	}
	if !strings.Contains(err.Error(), "other/bot") {
		t.Errorf("the error must name the blocking agent, got %q", err.Error())
	}
}
