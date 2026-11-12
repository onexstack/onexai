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

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// memoryClient is a small read-write client.Client.
//
// controller-runtime's fake client cannot be used in this repository: k8s
// v0.38 moved to structured-merge-diff v7 while the fake client still uses v6,
// and the two types do not convert (see internal/controller/agent/agent).
// The embedded interface is nil, so any method this test client does not
// implement panics — a clear signal that a test reached an unexpected path.
type memoryClient struct {
	client.Client
	objects []client.Object
	// failDelete makes Delete return this error once, so a retry can be tested.
	failDelete error
}

func (c *memoryClient) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	for _, object := range c.objects {
		if !sameType(object, obj) || object.GetNamespace() != key.Namespace || object.GetName() != key.Name {
			continue
		}
		copyInto(obj, object)
		return nil
	}
	return apierrors.NewNotFound(schema.GroupResource{Resource: "objects"}, key.Name)
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

func (c *memoryClient) Create(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
	for _, object := range c.objects {
		if sameType(object, obj) && object.GetNamespace() == obj.GetNamespace() && object.GetName() == obj.GetName() {
			return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "objects"}, obj.GetName())
		}
	}
	c.objects = append(c.objects, obj.DeepCopyObject().(client.Object))
	return nil
}

func (c *memoryClient) Delete(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
	if c.failDelete != nil {
		err := c.failDelete
		c.failDelete = nil
		return err
	}
	for i, object := range c.objects {
		if sameType(object, obj) && object.GetNamespace() == obj.GetNamespace() && object.GetName() == obj.GetName() {
			c.objects = append(c.objects[:i], c.objects[i+1:]...)
			return nil
		}
	}
	return apierrors.NewNotFound(schema.GroupResource{Resource: "objects"}, obj.GetName())
}

func (c *memoryClient) Update(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
	for i, object := range c.objects {
		if sameType(object, obj) && object.GetNamespace() == obj.GetNamespace() && object.GetName() == obj.GetName() {
			c.objects[i] = obj.DeepCopyObject().(client.Object)
			return nil
		}
	}
	return apierrors.NewNotFound(schema.GroupResource{Resource: "objects"}, obj.GetName())
}

func (c *memoryClient) Patch(_ context.Context, obj client.Object, patch client.Patch, _ ...client.PatchOption) error {
	for i, object := range c.objects {
		if !sameType(object, obj) || object.GetNamespace() != obj.GetNamespace() || object.GetName() != obj.GetName() {
			continue
		}
		raw, err := json.Marshal(object)
		if err != nil {
			return err
		}
		data, err := patch.Data(obj)
		if err != nil {
			return err
		}
		var base, changes map[string]any
		if err := json.Unmarshal(raw, &base); err != nil {
			return err
		}
		if err := json.Unmarshal(data, &changes); err != nil {
			return err
		}
		merged, err := json.Marshal(mergePatch(base, changes))
		if err != nil {
			return err
		}
		updated := object.DeepCopyObject().(client.Object)
		if err := json.Unmarshal(merged, updated); err != nil {
			return err
		}
		c.objects[i] = updated
		copyInto(obj, updated)
		return nil
	}
	return apierrors.NewNotFound(schema.GroupResource{Resource: "objects"}, obj.GetName())
}

func (c *memoryClient) DeleteAllOf(_ context.Context, _ client.Object, _ ...client.DeleteAllOfOption) error {
	return nil
}

func (c *memoryClient) Status() client.SubResourceWriter { return statusWriter{c: c} }

// statusWriter only needs to persist the object the reconciler patched; the
// tests assert on spec/labels, not on the status subresource semantics.
type statusWriter struct {
	c *memoryClient
}

func (w statusWriter) Create(_ context.Context, _ client.Object, _ client.Object, _ ...client.SubResourceCreateOption) error {
	return nil
}

func (w statusWriter) Update(_ context.Context, obj client.Object, _ ...client.SubResourceUpdateOption) error {
	return w.c.Update(context.Background(), obj)
}

func (w statusWriter) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	return w.c.Update(context.Background(), obj)
}

func (w statusWriter) Apply(_ context.Context, _ runtime.ApplyConfiguration, _ ...client.SubResourceApplyOption) error {
	return nil
}

func sameType(a, b any) bool {
	return reflect.TypeOf(a) == reflect.TypeOf(b)
}

func copyInto(dst, src client.Object) {
	reflect.ValueOf(dst).Elem().Set(reflect.ValueOf(src.DeepCopyObject()).Elem())
}

func mergePatch(dst, patch map[string]any) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for key, value := range patch {
		if value == nil {
			delete(dst, key)
			continue
		}
		patchMap, ok := value.(map[string]any)
		if !ok {
			dst[key] = value
			continue
		}
		existing, ok := dst[key].(map[string]any)
		if !ok {
			existing = map[string]any{}
		}
		dst[key] = mergePatch(existing, patchMap)
	}
	return dst
}

func fakeMCP(name string, enabled bool) *agentv1alpha1.MCPServer {
	return &agentv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "helper-public",
			UID:       types.UID("uid-" + name),
		},
		Spec: agentv1alpha1.MCPServerSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: enabled},
		},
	}
}

func newTestReconciler(recorder *record.FakeRecorder, objects ...client.Object) (*Reconciler, *memoryClient) {
	c := &memoryClient{objects: objects}
	r := &Reconciler{Client: c, APIReader: c}
	// Assign the recorder only when it exists: a typed nil pointer in the
	// interface would pass the nil check in Reconciler.event and then panic.
	if recorder != nil {
		r.Recorder = recorder
	}
	return r, c
}

func findRule(t *testing.T, c *memoryClient, ns, name string) *agentv1alpha1.AuthorizationRule {
	t.Helper()
	rule := &agentv1alpha1.AuthorizationRule{}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, rule)
	if apierrors.IsNotFound(err) {
		return nil
	}
	require.NoError(t, err)
	return rule
}

func TestEnsureAutoGrantCreatesConventionalRule(t *testing.T) {
	mcp := fakeMCP("otest", true)
	recorder := record.NewFakeRecorder(10)
	r, c := newTestReconciler(recorder, mcp)

	require.NoError(t, r.ensureAutoGrant(context.Background(), mcp))

	rule := findRule(t, c, "helper-public", "otest-grant")
	require.NotNil(t, rule)
	assert.Equal(t, "otest-grant", rule.Name)
	assert.Equal(t, agentv1alpha1.ManagedByMCPServerController, rule.Labels[agentv1alpha1.LabelGrantManagedBy])
	assert.Equal(t, "helper-public/otest", rule.Annotations[agentv1alpha1.AnnotationGrantRef])
	assert.Equal(t, "uid-otest", rule.Annotations[agentv1alpha1.AnnotationGrantOwnerUID])
	require.Len(t, rule.OwnerReferences, 1)
	assert.Equal(t, "MCPServer", rule.OwnerReferences[0].Kind)
	assert.True(t, rule.Spec.Enabled)
	assert.Equal(t, agentv1alpha1.EffectAllow, rule.Spec.Effect)
	assert.Equal(t, []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "*"}}, rule.Spec.Subjects)
	assert.Equal(t, agentv1alpha1.AuthorizationResource{Kind: "mcp", Names: []string{"otest", "helper-public/otest"}}, rule.Spec.Resource)
	assert.Equal(t, []string{"get", "list", "watch"}, rule.Spec.Verbs)
	assert.Empty(t, rule.Spec.AgentNames)
	assert.Nil(t, rule.Spec.AgentSelector)

	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "GrantRuleCreated")
	default:
		t.Fatal("no GrantRuleCreated event")
	}
}

func TestEnsureAutoGrantLeavesCoveringRuleUntouched(t *testing.T) {
	mcp := fakeMCP("otest", true)
	existing := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "otest-grant", Namespace: "helper-public"},
		Spec: agentv1alpha1.AuthorizationRuleSpec{
			Enabled:  true,
			Effect:   agentv1alpha1.EffectAllow,
			Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "*"}},
			Resource: agentv1alpha1.AuthorizationResource{Kind: "mcp", Names: []string{"otest"}},
			Verbs:    []string{"get", "list", "watch"},
		},
	}
	r, c := newTestReconciler(nil, mcp, existing)

	require.NoError(t, r.ensureAutoGrant(context.Background(), mcp))

	got := findRule(t, c, "helper-public", "otest-grant")
	require.NotNil(t, got)
	assert.Empty(t, got.Labels, "a foreign rule must not be relabelled")
	assert.Equal(t, existing.Spec, got.Spec)
}

func TestEnsureAutoGrantWarnsOnConflict(t *testing.T) {
	mcp := fakeMCP("otest", true)
	conflicting := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "otest-grant", Namespace: "helper-public"},
		Spec: agentv1alpha1.AuthorizationRuleSpec{
			Enabled:  true,
			Effect:   agentv1alpha1.EffectAllow,
			Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "*"}},
			Resource: agentv1alpha1.AuthorizationResource{Kind: "skill", Names: []string{"otest"}},
			Verbs:    []string{"get", "list", "watch"},
		},
	}
	recorder := record.NewFakeRecorder(10)
	r, c := newTestReconciler(recorder, mcp, conflicting)

	require.NoError(t, r.ensureAutoGrant(context.Background(), mcp))

	got := findRule(t, c, "helper-public", "otest-grant")
	require.NotNil(t, got)
	assert.Equal(t, "skill", got.Spec.Resource.Kind, "the conflicting rule must not be replaced")
	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "GrantRuleConflict")
	default:
		t.Fatal("no GrantRuleConflict event")
	}
}

func TestEnsureAutoGrantRepairsManagedRule(t *testing.T) {
	mcp := fakeMCP("otest", true)
	managed := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "otest-grant",
			Namespace: "helper-public",
			Labels: map[string]string{
				agentv1alpha1.LabelGrantManagedBy: agentv1alpha1.ManagedByMCPServerController,
			},
			Annotations: map[string]string{
				agentv1alpha1.AnnotationGrantRef: "helper-public/otest",
			},
		},
		Spec: agentv1alpha1.AuthorizationRuleSpec{
			Enabled:  false,
			Effect:   agentv1alpha1.EffectDeny,
			Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "someone"}},
			Resource: agentv1alpha1.AuthorizationResource{Kind: "skill", Names: []string{"other"}},
			Verbs:    []string{"write"},
		},
	}
	r, c := newTestReconciler(nil, mcp, managed)

	require.NoError(t, r.ensureAutoGrant(context.Background(), mcp))

	got := findRule(t, c, "helper-public", "otest-grant")
	require.NotNil(t, got)
	assert.Equal(t, agentv1alpha1.EffectAllow, got.Spec.Effect)
	assert.True(t, got.Spec.Enabled)
	assert.Equal(t, agentv1alpha1.AuthorizationResource{Kind: "mcp", Names: []string{"otest", "helper-public/otest"}}, got.Spec.Resource)
	assert.Contains(t, got.Annotations, agentv1alpha1.AnnotationGrantRef)
}

func TestEnsureAutoGrantOptOutRemovesManagedRule(t *testing.T) {
	mcp := fakeMCP("otest", true)
	mcp.Annotations = map[string]string{agentv1alpha1.MCPServerAnnotationAutoGrant: " false "}
	managed := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "otest-grant",
			Namespace: "helper-public",
			Labels: map[string]string{
				agentv1alpha1.LabelGrantManagedBy: agentv1alpha1.ManagedByMCPServerController,
			},
			Annotations: map[string]string{
				agentv1alpha1.AnnotationGrantRef: "helper-public/otest",
			},
		},
	}
	r, c := newTestReconciler(nil, mcp, managed)

	require.NoError(t, r.ensureAutoGrant(context.Background(), mcp))

	assert.Nil(t, findRule(t, c, "helper-public", "otest-grant"))
}

func TestEnsureAutoGrantOptOutKeepsForeignRule(t *testing.T) {
	mcp := fakeMCP("otest", true)
	mcp.Annotations = map[string]string{agentv1alpha1.MCPServerAnnotationAutoGrant: "false"}
	foreign := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "otest-grant", Namespace: "helper-public"},
		Spec: agentv1alpha1.AuthorizationRuleSpec{
			Enabled:  true,
			Effect:   agentv1alpha1.EffectAllow,
			Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "*"}},
			Resource: agentv1alpha1.AuthorizationResource{Kind: "mcp", Names: []string{"otest"}},
		},
	}
	r, c := newTestReconciler(nil, mcp, foreign)

	require.NoError(t, r.ensureAutoGrant(context.Background(), mcp))

	assert.NotNil(t, findRule(t, c, "helper-public", "otest-grant"))
}

func TestEnsureAutoGrantSkipsDisabledServer(t *testing.T) {
	mcp := fakeMCP("otest", false)
	r, c := newTestReconciler(nil, mcp)

	require.NoError(t, r.ensureAutoGrant(context.Background(), mcp))

	assert.Nil(t, findRule(t, c, "helper-public", "otest-grant"))
}

func TestEnsureAutoGrantWaitsForTerminatingRule(t *testing.T) {
	mcp := fakeMCP("otest", true)
	now := metav1.Now()
	terminating := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "otest-grant",
			Namespace:         "helper-public",
			DeletionTimestamp: &now,
			Labels: map[string]string{
				agentv1alpha1.LabelGrantManagedBy: agentv1alpha1.ManagedByMCPServerController,
			},
			Annotations: map[string]string{
				agentv1alpha1.AnnotationGrantRef: "helper-public/otest",
			},
		},
	}
	r, _ := newTestReconciler(nil, mcp, terminating)

	err := r.ensureAutoGrant(context.Background(), mcp)
	assert.True(t, errors.Is(err, errGrantTerminating), "got %v", err)
}

func TestEnsureAutoGrantNameTooLong(t *testing.T) {
	mcp := fakeMCP(strings.Repeat("a", 250), true)
	recorder := record.NewFakeRecorder(10)
	r, c := newTestReconciler(recorder, mcp)

	require.NoError(t, r.ensureAutoGrant(context.Background(), mcp))

	assert.Nil(t, findRule(t, c, "helper-public", agentv1alpha1.MCPGrantRuleName(mcp.Name)),
		"no rule can be created for an over-long name")
	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "GrantRuleNameTooLong")
	default:
		t.Fatal("no GrantRuleNameTooLong event")
	}
}

func TestCleanupAutoGrantRemovesOnlyManagedRules(t *testing.T) {
	managed := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "otest-grant",
			Namespace: "helper-public",
			Labels: map[string]string{
				agentv1alpha1.LabelGrantManagedBy: agentv1alpha1.ManagedByMCPServerController,
			},
			Annotations: map[string]string{
				agentv1alpha1.AnnotationGrantRef: "helper-public/otest",
			},
		},
	}
	foreign := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "other-grant", Namespace: "helper-public"},
	}
	r, c := newTestReconciler(nil, managed, foreign)

	require.NoError(t, r.cleanupAutoGrant(context.Background(), types.NamespacedName{Namespace: "helper-public", Name: "otest"}))
	require.NoError(t, r.cleanupAutoGrant(context.Background(), types.NamespacedName{Namespace: "helper-public", Name: "other"}))

	assert.Nil(t, findRule(t, c, "helper-public", "otest-grant"))
	assert.NotNil(t, findRule(t, c, "helper-public", "other-grant"))
}

func TestCleanupAutoGrantKeepsRuleWhenServerWasRecreated(t *testing.T) {
	mcp := fakeMCP("otest", true)
	managed := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "otest-grant",
			Namespace: "helper-public",
			Labels: map[string]string{
				agentv1alpha1.LabelGrantManagedBy: agentv1alpha1.ManagedByMCPServerController,
			},
			Annotations: map[string]string{
				agentv1alpha1.AnnotationGrantRef: "helper-public/otest",
			},
		},
	}
	// The cache (Client) does not have the recreated server yet; the uncached
	// reader does. The rule must survive.
	r, c := newTestReconciler(nil, managed)
	r.APIReader = &memoryClient{objects: []client.Object{mcp}}

	require.NoError(t, r.cleanupAutoGrant(context.Background(), types.NamespacedName{Namespace: "helper-public", Name: "otest"}))

	assert.NotNil(t, findRule(t, c, "helper-public", "otest-grant"))
}

func TestCleanupAutoGrantReturnsDeleteError(t *testing.T) {
	managed := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "otest-grant",
			Namespace: "helper-public",
			Labels: map[string]string{
				agentv1alpha1.LabelGrantManagedBy: agentv1alpha1.ManagedByMCPServerController,
			},
			Annotations: map[string]string{
				agentv1alpha1.AnnotationGrantRef: "helper-public/otest",
			},
		},
	}
	r, c := newTestReconciler(nil, managed)
	c.failDelete = errors.New("transient")

	err := r.cleanupAutoGrant(context.Background(), types.NamespacedName{Namespace: "helper-public", Name: "otest"})
	require.Error(t, err)
}

func TestGrantRuleCovers(t *testing.T) {
	mcp := fakeMCP("otest", true)

	rule := func(mutate func(*agentv1alpha1.AuthorizationRule)) *agentv1alpha1.AuthorizationRule {
		r := &agentv1alpha1.AuthorizationRule{Spec: agentv1alpha1.AuthorizationRuleSpec{
			Enabled:  true,
			Effect:   agentv1alpha1.EffectAllow,
			Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "*"}},
			Resource: agentv1alpha1.AuthorizationResource{Kind: "mcp", Names: []string{"otest"}},
		}}
		mutate(r)
		return r
	}

	tests := []struct {
		name string
		rule *agentv1alpha1.AuthorizationRule
		want bool
	}{
		{"baseline", rule(func(*agentv1alpha1.AuthorizationRule) {}), true},
		{"canonical name", rule(func(r *agentv1alpha1.AuthorizationRule) {
			r.Spec.Resource.Names = []string{"helper-public/otest"}
		}), true},
		{"wildcard name", rule(func(r *agentv1alpha1.AuthorizationRule) {
			r.Spec.Resource.Names = []string{"*"}
		}), true},
		{"disabled", rule(func(r *agentv1alpha1.AuthorizationRule) { r.Spec.Enabled = false }), false},
		{"deny", rule(func(r *agentv1alpha1.AuthorizationRule) { r.Spec.Effect = agentv1alpha1.EffectDeny }), false},
		{"other kind", rule(func(r *agentv1alpha1.AuthorizationRule) { r.Spec.Resource.Kind = "skill" }), false},
		{"other name", rule(func(r *agentv1alpha1.AuthorizationRule) {
			r.Spec.Resource.Names = []string{"other"}
		}), false},
		{"agent scoped", rule(func(r *agentv1alpha1.AuthorizationRule) {
			r.Spec.AgentNames = []string{"otest222"}
		}), false},
		{"specific user", rule(func(r *agentv1alpha1.AuthorizationRule) {
			r.Spec.Subjects = []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "someone"}}
		}), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, grantRuleCovers(tt.rule, mcp))
		})
	}
}

func TestAutoGrantOptedOut(t *testing.T) {
	tests := []struct {
		annotations map[string]string
		want        bool
	}{
		{nil, false},
		{map[string]string{agentv1alpha1.MCPServerAnnotationAutoGrant: "true"}, false},
		{map[string]string{agentv1alpha1.MCPServerAnnotationAutoGrant: "false"}, true},
		{map[string]string{agentv1alpha1.MCPServerAnnotationAutoGrant: " FALSE "}, true},
	}
	for _, tt := range tests {
		mcp := fakeMCP("otest", true)
		mcp.Annotations = tt.annotations
		assert.Equal(t, tt.want, autoGrantOptedOut(mcp), "annotations=%v", tt.annotations)
	}
}

func TestMCPGrantRuleName(t *testing.T) {
	assert.Equal(t, "otest-grant", agentv1alpha1.MCPGrantRuleName("otest"))
	assert.Equal(t, "helper-public", normalizeServerNamespace(""))
	assert.Equal(t, "other", normalizeServerNamespace("other"))
}
