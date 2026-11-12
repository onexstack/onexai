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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func TestNormalizeReferrers(t *testing.T) {
	assert.Equal(t, []string{"helper-public", "team-a"},
		normalizeReferrers([]string{"team-a", "", "helper-public", "team-a"}))
}

func TestSameNamespaceForAll(t *testing.T) {
	assert.True(t, sameNamespaceForAll("team-a", []string{"team-a"}))
	assert.False(t, sameNamespaceForAll("team-a", []string{"team-a", "team-b"}))
	assert.False(t, sameNamespaceForAll("team-a", []string{"team-b"}))
}

// staticGrants is a ReferenceGrant store that answers List from a fixed slice.
// The unused methods come from the embedded empty store.
type staticGrants struct {
	store.ReferenceGrantStore
	grants []*agentv1alpha1.ReferenceGrant
}

func (s staticGrants) List(context.Context, int, int) (int64, []*agentv1alpha1.ReferenceGrant, error) {
	return int64(len(s.grants)), s.grants, nil
}

// staticBindings is the ClusterAgentBinding counterpart of staticGrants.
type staticBindings struct {
	store.ClusterAgentBindingStore
	bindings []*agentv1alpha1.ClusterAgentBinding
}

func (s staticBindings) List(
	context.Context, int, int,
) (int64, []*agentv1alpha1.ClusterAgentBinding, error) {
	return int64(len(s.bindings)), s.bindings, nil
}

// policyStore is the fake resource store plus the two inputs the namespace gate
// reads. Everything else keeps the instruction-test fake's behaviour.
type policyStore struct {
	*fakeAIStore
	grants   staticGrants
	bindings staticBindings
}

func (s policyStore) ReferenceGrants() store.ReferenceGrantStore { return s.grants }
func (s policyStore) ClusterAgentBindings() store.ClusterAgentBindingStore {
	return s.bindings
}

func newPolicyStore() *policyStore {
	return &policyStore{
		fakeAIStore: newFakeResources(),
		grants:      staticGrants{ReferenceGrantStore: store.NewEmptyStore().ReferenceGrants()},
		bindings:    staticBindings{ClusterAgentBindingStore: store.NewEmptyStore().ClusterAgentBindings()},
	}
}

func grant(fromNamespace, targetKind, targetName string) *agentv1alpha1.ReferenceGrant {
	return &agentv1alpha1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant", Namespace: "team-b"},
		Spec: agentv1alpha1.ReferenceGrantSpec{
			From: []agentv1alpha1.ReferenceGrantFrom{{
				Group: sharing.GroupName, Kind: sharing.KindAgent, Namespace: fromNamespace,
			}},
			To: []agentv1alpha1.ReferenceGrantTo{{
				Group: sharing.GroupName, Kind: targetKind, Name: targetName,
			}},
		},
	}
}

func TestCheckReferencePolicies(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		referrers []string
		spec      RunSpec
		grants    []*agentv1alpha1.ReferenceGrant
		bindings  []*agentv1alpha1.ClusterAgentBinding
		composed  bool
		wantErr   bool
		wantComp  bool
	}{
		{
			name:      "same namespace needs no grant",
			referrers: []string{"team-b"},
			spec:      RunSpec{MCPs: []string{"team-b/onexai"}},
		},
		{
			name:      "cross namespace without a grant is refused",
			referrers: []string{"team-a"},
			spec:      RunSpec{MCPs: []string{"team-b/onexai"}},
			composed:  true,
			wantErr:   true,
			wantComp:  true,
		},
		{
			name:      "cross namespace with a grant is admitted",
			referrers: []string{"team-a"},
			spec:      RunSpec{MCPs: []string{"team-b/onexai"}},
			grants:    []*agentv1alpha1.ReferenceGrant{grant("team-a", sharing.KindMCPServer, "onexai")},
		},
		{
			name:      "every referrer must be admitted",
			referrers: []string{"team-a", "team-c"},
			spec:      RunSpec{MCPs: []string{"team-b/onexai"}},
			grants:    []*agentv1alpha1.ReferenceGrant{grant("team-a", sharing.KindMCPServer, "onexai")},
			composed:  true,
			wantErr:   true,
			wantComp:  true,
		},
		{
			name:      "a custom command is never shareable",
			referrers: []string{"team-a"},
			spec:      RunSpec{CustomCommands: []string{"team-b/triage"}},
			grants:    []*agentv1alpha1.ReferenceGrant{grant("team-a", sharing.KindCustomCommand, "triage")},
			composed:  true,
			wantErr:   true,
			wantComp:  true,
		},
		{
			name:      "a custom command in the referrer namespace is fine",
			referrers: []string{"team-a"},
			spec:      RunSpec{CustomCommands: []string{"team-a/triage"}},
		},
		{
			name:      "a granted prompt is admitted",
			referrers: []string{"team-a"},
			spec:      RunSpec{PromptRefs: []string{"team-b/persona"}},
			grants:    []*agentv1alpha1.ReferenceGrant{grant("team-a", sharing.KindPrompt, "persona")},
		},
		{
			name:      "a granted model is admitted",
			referrers: []string{"team-a"},
			spec:      RunSpec{Models: []string{"team-b/deepseek"}},
			grants:    []*agentv1alpha1.ReferenceGrant{grant("team-a", sharing.KindModel, "deepseek")},
		},
		{
			name:      "a cluster binding is an administrator's grant",
			referrers: []string{"team-a"},
			spec:      RunSpec{MCPs: []string{"team-b/onexai"}},
			bindings: []*agentv1alpha1.ClusterAgentBinding{{
				Spec: agentv1alpha1.ClusterAgentBindingSpec{
					Namespaces: []string{"team-a"},
					Agents:     []agentv1alpha1.Reference{{Namespace: "team-a", Name: "helper"}},
					MCPServers: []agentv1alpha1.Reference{{Namespace: "team-b", Name: "onexai"}},
				},
			}},
			composed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resources := newPolicyStore()
			resources.grants.grants = tc.grants
			resources.bindings.bindings = tc.bindings
			resolver := NewResolver(resources, nil)

			var records []*model.AgentM
			for _, referrer := range tc.referrers {
				records = append(records, &model.AgentM{Name: "helper", Namespace: referrer})
			}
			err := resolver.checkReferencePolicies(ctx, records, tc.referrers, tc.spec, tc.composed)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var compErr *CompositionError
			assert.Equal(t, tc.wantComp, errors.As(err, &compErr))
		})
	}
}
