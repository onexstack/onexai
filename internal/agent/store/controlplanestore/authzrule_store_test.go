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

package controlplanestore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzrule"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient/declstore"
	versionedfake "github.com/onexstack/onexai/pkg/generated/clientset/versioned/fake"
	informers "github.com/onexstack/onexai/pkg/generated/informers/externalversions"
	agentlisters "github.com/onexstack/onexai/pkg/generated/listers/agent/v1alpha1"
)

// testNamespace stands in for the default namespace the production rules live in.
const testNamespace = "helper-public"

// newTestStore wires the store over a fake API server and a real informer, so
// the write goes through the client and the read comes back from the cache —
// the same split production has.
func newTestStore(t *testing.T, namespace string) (*AuthzRuleStore, *versionedfake.Clientset) {
	t.Helper()

	clientset := versionedfake.NewSimpleClientset()
	factory := informers.NewSharedInformerFactory(clientset, 0)
	factory.Agent().V1alpha1().AuthorizationRules().Informer()

	stop := make(chan struct{})
	factory.Start(stop)
	t.Cleanup(func() { close(stop) })

	for typ, synced := range factory.WaitForCacheSync(stop) {
		require.True(t, synced, "informer for %v did not sync", typ)
	}

	lister := agentlisters.NewAuthorizationRuleLister(
		factory.Agent().V1alpha1().AuthorizationRules().Informer().GetIndexer())
	return newAuthzRuleStore(clientset.AgentV1alpha1(), lister, namespace), clientset
}

// namespaceGrant is the row OneX writes before every console run, field for
// field (see onex agentaccount.ensureWorkspaceRule).
func namespaceGrant() *model.AuthzRuleM {
	return &model.AuthzRuleM{
		Name:         "onex-workspace-grant",
		SubjectType:  "user",
		SubjectID:    model.StringList{"*"},
		ResourceKind: "namespace",
		ResourceName: model.StringList{"onex"},
		Verbs:        model.StringList{"read"},
		Effect:       "allow",
		Status:       model.StatusEnabled,
		Description:  "OneX platform namespace grant",
	}
}

func TestAuthzRuleStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, clientset := newTestStore(t, testNamespace)

	require.NoError(t, store.Create(ctx, namespaceGrant()))

	// The object must exist where the migration put every other rule, and the
	// API server must have accepted it as a normal AuthorizationRule.
	obj, err := clientset.AgentV1alpha1().AuthorizationRules(testNamespace).Get(ctx, namespaceGrant().Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "namespace", obj.Spec.Resource.Kind)
	assert.Equal(t, []string{"get", "list", "watch"}, obj.Spec.Verbs)
	require.Len(t, obj.Spec.Subjects, 1)
	assert.Equal(t, rbacv1.UserKind, obj.Spec.Subjects[0].Kind)
	assert.Equal(t, "*", obj.Spec.Subjects[0].Name)

	row, err := store.Get(ctx, where.F("name", namespaceGrant().Name))
	require.NoError(t, err)
	assert.Equal(t, "user", row.SubjectType)
	assert.Equal(t, model.StringList{"*"}, row.SubjectID)
	assert.Equal(t, model.StringList{"read"}, row.Verbs)
	assert.Equal(t, model.StatusEnabled, row.Status)

	// A rewrite keeps the object in place and takes the row's new scope.
	updated := *row
	updated.ResourceName = model.StringList{"onex-console"}
	updated.Verbs = model.StringList{"read", "write"}
	require.NoError(t, store.Update(ctx, &updated))

	obj, err = clientset.AgentV1alpha1().AuthorizationRules(testNamespace).Get(ctx, updated.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"onex-console"}, obj.Spec.Resource.Names)
	assert.Equal(t, []string{"get", "list", "watch", "create", "update", "patch", "delete"}, obj.Spec.Verbs)

	require.NoError(t, store.Delete(ctx, where.F("name", updated.Name)))
	_, err = store.Get(ctx, where.F("name", updated.Name))
	assert.ErrorIs(t, err, agentstore.ErrNotFound)
}

func TestAuthzRuleStoreGetMissing(t *testing.T) {
	store, _ := newTestStore(t, testNamespace)

	// authzrule.Service reads this as "absent, create it"; anything else makes
	// every provisioning call fail.
	_, err := store.Get(context.Background(), where.F("name", "no-such-rule"))
	assert.ErrorIs(t, err, agentstore.ErrNotFound)
}

func TestAuthzRuleStoreFindsObjectInAnotherNamespace(t *testing.T) {
	ctx := context.Background()
	store, clientset := newTestStore(t, testNamespace)

	// An operator's kubectl apply, or the migration, may have placed the rule
	// somewhere else. The store reads cluster-wide, so it must find it rather
	// than create a second object with the same name.
	foreign := namespaceGrant()
	_, err := clientset.AgentV1alpha1().AuthorizationRules("onex").Create(ctx,
		mustObject(t, foreign, "onex"), metav1.CreateOptions{})
	require.NoError(t, err)
	waitForRule(t, store, foreign.Name)

	foreign.ResourceName = model.StringList{"renamed-namespace"}
	require.NoError(t, store.Update(ctx, foreign))

	obj, err := clientset.AgentV1alpha1().AuthorizationRules("onex").Get(ctx, foreign.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"renamed-namespace"}, obj.Spec.Resource.Names)

	// Nothing was created in the write namespace: one name, one object.
	_, err = clientset.AgentV1alpha1().AuthorizationRules(testNamespace).Get(ctx, foreign.Name, metav1.GetOptions{})
	assert.True(t, isNotFound(err))

	require.NoError(t, store.Delete(ctx, where.F("name", foreign.Name)))
}

func TestAuthzRuleStoreListFiltersAndPages(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t, testNamespace)

	rows := []*model.AuthzRuleM{
		{Name: "a-user-grant", SubjectType: "user", SubjectID: model.StringList{"user-1"},
			ResourceKind: "agent", ResourceName: model.StringList{"onex"}, Verbs: model.StringList{"read"},
			Effect: "allow", Status: model.StatusEnabled},
		{Name: "b-user-grant", SubjectType: "user", SubjectID: model.StringList{"user-2"},
			ResourceKind: "agent", ResourceName: model.StringList{"onex"}, Verbs: model.StringList{"read"},
			Effect: "allow", Status: model.StatusEnabled},
		{Name: "c-deny", SubjectType: "group", SubjectID: model.StringList{"fz-public"},
			ResourceKind: "mcp", ResourceName: model.StringList{"zke"}, Verbs: model.StringList{"read"},
			Effect: "deny", Status: model.StatusDisabled},
	}
	for _, row := range rows {
		require.NoError(t, store.Create(ctx, row))
	}

	total, all, err := store.List(ctx, where.L(-1))
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, all, 3)
	assert.Equal(t, "a-user-grant", all[0].Name, "listing must be ordered by name")

	total, filtered, err := store.List(ctx, where.F("subjectType", "user"))
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, filtered, 2)

	total, denied, err := store.List(ctx, where.F("effect", "deny"))
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, denied, 1)
	assert.Equal(t, model.StatusDisabled, denied[0].Status)

	total, page, err := store.List(ctx, where.O(1).L(1))
	require.NoError(t, err)
	assert.Equal(t, int64(3), total, "the total is the filtered count, not the page size")
	require.Len(t, page, 1)
	assert.Equal(t, "b-user-grant", page[0].Name)

	_, _, err = store.List(ctx, where.F("mysteryColumn", "x"))
	assert.ErrorIs(t, err, declstore.ErrUnsupportedQuery)
}

func TestAuthzRuleStoreRoundTripsMixedSubjectKinds(t *testing.T) {
	ctx := context.Background()
	store, clientset := newTestStore(t, testNamespace)

	// The plural shape carries a user and a group in one policy. The read path
	// must keep both; collapsing to one kind would silently change who the
	// policy speaks about.
	mixed := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "mixed", Namespace: testNamespace},
		Spec: agentv1alpha1.AuthorizationRuleSpec{
			Subjects: []rbacv1.Subject{
				{Kind: rbacv1.UserKind, Name: "user-1"},
				{Kind: rbacv1.GroupKind, Name: "ops"},
			},
			Resource: agentv1alpha1.AuthorizationResource{Kind: "agent", Names: []string{"onex"}},
			Verbs:    []string{"get"},
			Effect:   agentv1alpha1.EffectAllow,
			Enabled:  true,
		},
	}
	_, err := clientset.AgentV1alpha1().AuthorizationRules(testNamespace).Create(ctx, mixed, metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		total, _, err := store.List(ctx, where.L(-1))
		return err == nil && total == 1
	}, observeTimeout, observeInterval)

	row, err := store.Get(ctx, where.F("name", "mixed"))
	require.NoError(t, err)
	require.Len(t, row.Subjects, 2)
	assert.ElementsMatch(t, []string{"user", "group"}, []string{
		row.Subjects[0].Kind, row.Subjects[1].Kind,
	})
	// A mixed policy has no single-kind spelling; the flat columns stay empty
	// and the plural list is the complete statement.
	assert.Empty(t, row.SubjectType)
	assert.Equal(t, []string{"agent"}, []string{row.Resources[0].Kind})
}

// TestAuthzRuleServiceOverControlPlane drives the service OneX provisions
// through — authzrule.Service — against the control-plane store. This is the
// path that answered 500 "authz rule API is unavailable" before, and the
// sequence is exactly the one OneX performs before every console message.
func TestAuthzRuleServiceOverControlPlane(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t, testNamespace)
	service := authzrule.New(store, nil)

	require.NoError(t, service.Create(ctx, namespaceGrant()))

	// The idempotent retry OneX performs after a failed create: a second create
	// is a duplicate, and the follow-up Get finds the rule.
	err := service.Create(ctx, namespaceGrant())
	require.Error(t, err)
	_, found, err := service.Get(ctx, namespaceGrant().Name)
	require.NoError(t, err)
	assert.True(t, found)

	rows, err := service.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	updated := *rows[0]
	updated.ResourceName = model.StringList{"onex-console"}
	require.NoError(t, service.Update(ctx, &updated))

	_, found, err = service.Get(ctx, updated.Name)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, model.StringList{"onex-console"}, updated.ResourceName)

	require.NoError(t, service.Delete(ctx, updated.Name))
	_, found, err = service.Get(ctx, updated.Name)
	require.NoError(t, err)
	assert.False(t, found)

	// Deleting an absent rule is ErrNotFound, which is what the handler turns
	// into a 404.
	assert.ErrorIs(t, service.Delete(ctx, updated.Name), authzrule.ErrNotFound)
}

func TestAuthzRuleConversions(t *testing.T) {
	row := &model.AuthzRuleM{
		Name:               "onex-ops-user-9iz492",
		SubjectType:        "user",
		SubjectID:          model.StringList{"user-9iz492"},
		ResourceKind:       "agent",
		ResourceName:       model.StringList{"onex-ops-console"},
		AgentNames:         model.StringList{"*"},
		Verbs:              model.StringList{"read", "write"},
		Effect:             "allow",
		Status:             model.StatusEnabled,
		Description:        "console assistant grant",
		ResourceAttributes: `{"env":"prod"}`,
	}

	cr := &agentv1alpha1.AuthorizationRule{ObjectMeta: metav1.ObjectMeta{Name: row.Name}}
	require.NoError(t, applyAuthzRule(row, cr))

	assert.Equal(t, []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "user-9iz492"}}, cr.Spec.Subjects)
	assert.Equal(t, []string{"get", "list", "watch", "create", "update", "patch", "delete"}, cr.Spec.Verbs)
	assert.Equal(t, agentv1alpha1.EffectAllow, cr.Spec.Effect)
	assert.True(t, cr.Spec.Enabled)
	require.NotNil(t, cr.Spec.AgentSelector, "the row's any-agent wildcard is the empty selector")
	assert.Empty(t, cr.Spec.AgentNames)
	require.NotNil(t, cr.Spec.Attributes)
	assert.JSONEq(t, `{"env":"prod"}`, string(cr.Spec.Attributes.Raw))

	back, err := authzRuleFromObject(cr)
	require.NoError(t, err)
	assert.Equal(t, row.SubjectType, back.SubjectType)
	assert.Equal(t, row.SubjectID, back.SubjectID)
	assert.Equal(t, row.ResourceKind, back.ResourceKind)
	assert.Equal(t, row.ResourceName, back.ResourceName)
	assert.Equal(t, model.StringList{"*"}, back.AgentNames)
	assert.Equal(t, model.StringList{"read", "write"}, back.Verbs)
	assert.Equal(t, `{"env":"prod"}`, back.ResourceAttributes)
	assert.Equal(t, model.StatusEnabled, back.Status)
}

func TestAuthzRuleConversionsDisabledAndNamespaceSubject(t *testing.T) {
	row := &model.AuthzRuleM{
		Name:         "ns-rule",
		SubjectType:  "namespace",
		SubjectID:    model.StringList{"helper-public"},
		ResourceKind: "namespace",
		ResourceName: model.StringList{"onex"},
		Verbs:        model.StringList{"read"},
		Effect:       "deny",
		Status:       model.StatusDisabled,
	}
	cr := &agentv1alpha1.AuthorizationRule{ObjectMeta: metav1.ObjectMeta{Name: row.Name}}
	require.NoError(t, applyAuthzRule(row, cr))
	assert.Equal(t, "helper-public", cr.Spec.SubjectNamespace)
	assert.Empty(t, cr.Spec.Subjects)
	assert.False(t, cr.Spec.Enabled)
	assert.Equal(t, agentv1alpha1.EffectDeny, cr.Spec.Effect)
	assert.Nil(t, cr.Spec.Attributes)

	back, err := authzRuleFromObject(cr)
	require.NoError(t, err)
	assert.Equal(t, "namespace", back.SubjectType)
	assert.Equal(t, model.StringList{"helper-public"}, back.SubjectID)
	assert.Equal(t, model.StatusDisabled, back.Status)
	assert.Equal(t, "deny", back.Effect)
}

func TestAuthzRuleConversionsRejectBadRows(t *testing.T) {
	cases := map[string]*model.AuthzRuleM{
		"no subject":     {Name: "x", SubjectType: "user", ResourceKind: "agent", ResourceName: model.StringList{"a"}, Verbs: model.StringList{"read"}, Effect: "allow"},
		"unknown type":   {Name: "x", SubjectType: "robot", SubjectID: model.StringList{"r"}, ResourceKind: "agent", ResourceName: model.StringList{"a"}, Verbs: model.StringList{"read"}, Effect: "allow"},
		"two namespaces": {Name: "x", SubjectType: "namespace", SubjectID: model.StringList{"a", "b"}, ResourceKind: "agent", ResourceName: model.StringList{"a"}, Verbs: model.StringList{"read"}, Effect: "allow"},
		"no verbs":       {Name: "x", SubjectType: "user", SubjectID: model.StringList{"u"}, ResourceKind: "agent", ResourceName: model.StringList{"a"}, Effect: "allow"},
		"bad attributes": {Name: "x", SubjectType: "user", SubjectID: model.StringList{"u"}, ResourceKind: "agent", ResourceName: model.StringList{"a"}, Verbs: model.StringList{"read"}, Effect: "allow", ResourceAttributes: "{"},
	}
	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			cr := &agentv1alpha1.AuthorizationRule{ObjectMeta: metav1.ObjectMeta{Name: row.Name}}
			assert.Error(t, applyAuthzRule(row, cr))
		})
	}
}

func TestAuthzRuleAttributesSurviveTheObjectRoundTrip(t *testing.T) {
	cr := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "attrs"},
		Spec: agentv1alpha1.AuthorizationRuleSpec{
			Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "u"}},
			Resource: agentv1alpha1.AuthorizationResource{Kind: "tool", Names: []string{"kubectl_*"}},
			Verbs:    []string{"use"},
			Effect:   agentv1alpha1.EffectAllow,
			Enabled:  true,
			Attributes: &runtime.RawExtension{
				Raw: []byte(`{"env":["prod","staging"]}`),
			},
		},
	}
	row, err := authzRuleFromObject(cr)
	require.NoError(t, err)
	assert.JSONEq(t, `{"env":["prod","staging"]}`, row.ResourceAttributes)
	assert.Equal(t, model.StringList{"use"}, row.Verbs)
}

func mustObject(t *testing.T, row *model.AuthzRuleM, namespace string) *agentv1alpha1.AuthorizationRule {
	t.Helper()
	cr := &agentv1alpha1.AuthorizationRule{ObjectMeta: metav1.ObjectMeta{Name: row.Name, Namespace: namespace}}
	require.NoError(t, applyAuthzRule(row, cr))
	return cr
}

// waitForRule waits for the informer to observe a name, so a read that follows
// a direct clientset write is not racing the watch.
func waitForRule(t *testing.T, store *AuthzRuleStore, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := store.Get(context.Background(), where.F("name", name))
		return err == nil
	}, observeTimeout, observeInterval)
}
