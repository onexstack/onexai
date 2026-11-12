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

// Package k8s adapts the platform's domain authorization request to the
// Kubernetes authorizer interface, and back.
//
// The platform keeps its domain model (Agent/Tool/Resource, read/write) because
// that is what the run path asks about. Kubernetes keeps its AttributesRecord
// because that is what a Webhook authorizer, RBAC or an external PDP already
// understands. This package is the only translation between them: a second
// translation would let a review endpoint and a run disagree about the same
// question.
package k8s

import (
	"context"
	"strings"

	"k8s.io/apiserver/pkg/authentication/user"
	kauthz "k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/union"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// Adapter makes a Kubernetes authorizer usable where the platform expects an
// authz.Authorizer.
type Adapter struct {
	Authorizer kauthz.Authorizer
}

var _ authz.Authorizer = Adapter{}

// Name implements authz.Authorizer.
func (a Adapter) Name() string { return "k8s" }

// Authorize implements authz.Authorizer.
func (a Adapter) Authorize(ctx context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
	if a.Authorizer == nil {
		return authz.DecisionNoOpinion, "", nil
	}
	decision, reason, err := a.Authorizer.Authorize(ctx, Attributes(req))
	switch decision {
	case kauthz.DecisionAllow:
		return authz.DecisionAllow, reason, err
	case kauthz.DecisionDeny:
		return authz.DecisionDeny, reason, err
	default:
		return authz.DecisionNoOpinion, reason, err
	}
}

// NamedAuthorizer is kube-apiserver's named authorizer, re-exported so callers
// of Union do not import two packages to compose one chain.
type NamedAuthorizer = union.NamedAuthorizer

// Union composes Kubernetes authorizers with kube-apiserver's ordered,
// first-non-NoOpinion semantics. The platform's own authorizer is placed first
// by the caller; a NoOpinion from it lets the external authorizers answer, and
// the caller still fails closed when all of them abstain.
func Union(authorizers ...NamedAuthorizer) (kauthz.Authorizer, error) {
	return union.New(authorizers...)
}

// Attributes translates one platform request to the Kubernetes vocabulary.
//
// The mapping is deliberately lossless in the direction a policy needs: the
// platform's Agent/Tool/Resource fields travel both as ResourceAttributes and
// as onexai.io/* Extra values, so a policy can address either the generic
// shape or a platform-specific field.
func Attributes(req authz.AuthorizationRequest) *kauthz.AttributesRecord {
	kind, name := req.EffectiveResource()

	attributes := req.Resource.Attributes
	namespace := ""
	subresource := ""
	if attributes != nil {
		namespace = attributes["namespace"]
		subresource = attributes["subresource"]
	}

	extra := map[string][]string{}
	for key, value := range req.Subject.Extra {
		extra[key] = splitCommaList(value)
	}
	extra["onexai.io/subject-kind"] = []string{string(req.Subject.Kind)}
	if req.AgentName != "" {
		extra["onexai.io/agent-name"] = []string{req.AgentName}
	}
	if names := req.EffectiveAgentNames(); len(names) > 0 {
		extra["onexai.io/agent-names"] = names
	}
	if req.ToolName != "" {
		extra["onexai.io/tool-name"] = []string{req.ToolName}
	}
	if req.MCPName != "" {
		extra["onexai.io/mcp-name"] = []string{req.MCPName}
	}
	for key, value := range req.Resource.Attributes {
		extra["onexai.io/attr/"+key] = []string{value}
	}

	return &kauthz.AttributesRecord{
		User: &user.DefaultInfo{
			Name:   req.Subject.PrimaryID(),
			Groups: groupList(req.Subject),
			Extra:  extra,
		},
		Verb:            sarVerb(req.Verb),
		Namespace:       namespace,
		APIGroup:        "onexai.io",
		APIVersion:      "v1",
		Resource:        string(kind),
		Subresource:     subresource,
		Name:            name,
		ResourceRequest: true,
	}
}

// sarVerb maps the platform's two-verb model onto the Kubernetes vocabulary.
func sarVerb(verb authz.Verb) string {
	if verb == authz.VerbWrite {
		return "update"
	}
	return "get"
}

// groupList exposes the group codes the platform carries on a user subject.
func groupList(subj authz.Subject) []string {
	if subj.Kind != authz.SubjectKindUser {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, key := range []string{"group_codes", "oncall_group_codes"} {
		for _, code := range splitCommaList(subj.Extra[key]) {
			if _, ok := seen[code]; ok {
				continue
			}
			seen[code] = struct{}{}
			out = append(out, code)
		}
	}
	return out
}

func splitCommaList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}
