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

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kauthz "k8s.io/apiserver/pkg/authorization/authorizer"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

func TestAttributes(t *testing.T) {
	attrs := Attributes(authz.AuthorizationRequest{
		Subject: authz.Subject{
			Kind:   authz.SubjectKindUser,
			UserID: "go.kong",
			Extra:  map[string]string{"group_codes": "g1,g2"},
		},
		Verb:      authz.VerbWrite,
		AgentName: "helper",
		ToolName:  "restart",
		MCPName:   "k8sgpt",
		Resource: authz.Resource{
			Kind: authz.ResourceKindService,
			Name: "payment",
			Attributes: map[string]string{
				"namespace":   "payments",
				"subresource": "status",
				"env":         "prod",
			},
		},
	})

	assert.Equal(t, "update", attrs.GetVerb())
	assert.Equal(t, "onexai.io", attrs.GetAPIGroup())
	assert.Equal(t, "v1", attrs.GetAPIVersion())
	assert.Equal(t, "service", attrs.GetResource())
	assert.Equal(t, "payment", attrs.GetName())
	assert.Equal(t, "payments", attrs.GetNamespace())
	assert.Equal(t, "status", attrs.GetSubresource())
	assert.True(t, attrs.IsResourceRequest())

	require.NotNil(t, attrs.GetUser())
	assert.Equal(t, "go.kong", attrs.GetUser().GetName())
	assert.ElementsMatch(t, []string{"g1", "g2"}, attrs.GetUser().GetGroups())
	assert.Equal(t, []string{"helper"}, attrs.GetUser().GetExtra()["onexai.io/agent-name"])
	assert.Equal(t, []string{"restart"}, attrs.GetUser().GetExtra()["onexai.io/tool-name"])
	assert.Equal(t, []string{"k8sgpt"}, attrs.GetUser().GetExtra()["onexai.io/mcp-name"])
	assert.Equal(t, []string{"prod"}, attrs.GetUser().GetExtra()["onexai.io/attr/env"])
}

func TestAttributesSynthesizesTheAgentResource(t *testing.T) {
	attrs := Attributes(authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "go.kong"},
		Verb:      authz.VerbRead,
		AgentName: "helper",
	})
	assert.Equal(t, "get", attrs.GetVerb())
	assert.Equal(t, "agent", attrs.GetResource())
	assert.Equal(t, "helper", attrs.GetName())
}

func TestAdapterMapsDecisions(t *testing.T) {
	tests := []struct {
		name     string
		decision kauthz.Decision
		want     authz.Decision
	}{
		{name: "allow", decision: kauthz.DecisionAllow, want: authz.DecisionAllow},
		{name: "deny", decision: kauthz.DecisionDeny, want: authz.DecisionDeny},
		{name: "no opinion", decision: kauthz.DecisionNoOpinion, want: authz.DecisionNoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := Adapter{Authorizer: kauthz.AuthorizerFunc(
				func(context.Context, kauthz.Attributes) (kauthz.Decision, string, error) {
					return tt.decision, tt.name, nil
				},
			)}
			got, reason, err := adapter.Authorize(context.Background(), authz.AuthorizationRequest{
				Subject: authz.Subject{Kind: authz.SubjectKindUser, UserID: "u"},
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.name, reason)
		})
	}
}

func TestUnionUsesTheFirstNonNoOpinionDecision(t *testing.T) {
	noOpinion := kauthz.AuthorizerFunc(func(context.Context, kauthz.Attributes) (kauthz.Decision, string, error) {
		return kauthz.DecisionNoOpinion, "", nil
	})
	allow := kauthz.AuthorizerFunc(func(context.Context, kauthz.Attributes) (kauthz.Decision, string, error) {
		return kauthz.DecisionAllow, "allowed", nil
	})
	deny := kauthz.AuthorizerFunc(func(context.Context, kauthz.Attributes) (kauthz.Decision, string, error) {
		return kauthz.DecisionDeny, "denied", nil
	})

	chain, err := Union(
		NamedAuthorizer{AuthorizerName: "no-opinion", Authorizer: noOpinion},
		NamedAuthorizer{AuthorizerName: "allow", Authorizer: allow},
	)
	require.NoError(t, err)
	decision, _, err := chain.Authorize(context.Background(), &kauthz.AttributesRecord{})
	require.NoError(t, err)
	assert.Equal(t, kauthz.DecisionAllow, decision)

	chain, err = Union(
		NamedAuthorizer{AuthorizerName: "deny", Authorizer: deny},
		NamedAuthorizer{AuthorizerName: "allow", Authorizer: allow},
	)
	require.NoError(t, err)
	decision, _, err = chain.Authorize(context.Background(), &kauthz.AttributesRecord{})
	require.NoError(t, err)
	assert.Equal(t, kauthz.DecisionDeny, decision)
}
