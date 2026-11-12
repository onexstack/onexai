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

package opa

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/open-policy-agent/opa/v1/rego"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

// buildRegoInput flattens an authz.AuthorizationRequest into the `input`
// document consumed by policy/authz.rego. The exact same shape is used for
// Agent-level (resource_kind="agent"), namespace-level (resource_kind=
// "namespace", or the legacy "workspace"), Tool-level (resource_kind="tool"),
// and Resource-ref-level
// (resource_kind="skill"|"mcp"|"knowledgebase", see
// authz.Coordinator.AuthorizeResourceRefs) requests — the Rego policy does not
// need separate modules per level. Likewise, subject.kind flows through
// verbatim regardless of SubjectKind (including "serviceaccount") — the
// policy's subject_matches has no per-kind special-casing beyond the
// group/namespace inheritance rules.
//
// resource_name is the fully-qualified spelling the runtime addresses, which
// for a namespaced reference is "namespace/name". resource_name_bare carries
// the same resource without the qualifier so a rule that names the object
// alone (the historical contract the seed and OneX's reconcile path write)
// still matches; see the Rego policy's resource_matches for the direction of
// that compatibility.
func buildRegoInput(req authz.AuthorizationRequest) map[string]any {
	// Agent-level and Tool-level requests carry no authz.Resource (that field
	// is reserved for resource/namespace-level checks) — EffectiveResource
	// synthesizes the addressing pair from AgentName/ToolName so the same Rego
	// rule_matches logic applies uniformly across every level.
	resourceKind, resourceName := req.EffectiveResource()

	subject := map[string]any{
		"kind":       string(req.Subject.Kind),
		"primary_id": req.Subject.PrimaryID(),
		// group_codes is the union of every group source the caller knows
		// about: user groups (console-managed membership) and on-call duty
		// groups. The policy inherits a user's group rules from this one list.
		"group_codes": groupCodes(req.Subject),
		// Retained for callers and logs that still read the historical name.
		"oncall_group_codes": onCallGroupCodes(req.Subject),
		// group_namespace is the namespace the requesting duty-group Subject
		// belongs to, used by the group-inherits-namespace Rego rule. Empty
		// when Subject is not a Group, or its namespace is unknown/unset.
		"group_namespace": req.Subject.Extra["group_namespace"],
	}

	// agent_names is the list agent-scoped rules are matched against;
	// agent_name stays for logs and for the reason strings a reader sees. A
	// merged multi-Agent run has no single name, so a rule scoped to one of its
	// Agents must still apply — the deny is the load-bearing direction.
	agentNames := req.EffectiveAgentNames()
	if agentNames == nil {
		agentNames = []string{}
	}

	return map[string]any{
		"subject":       subject,
		"verb":          string(req.Verb),
		"resource_kind": string(resourceKind),
		"resource_name": resourceName,
		// resource_name_bare lets a rule keep naming a namespaced resource
		// without its namespace ("onexai"), while the request itself carries
		// the canonical "namespace/name" the runtime addresses. For requests
		// whose name has no qualifier (agent, tool, namespace) the two fields
		// are equal.
		"resource_name_bare":  req.EffectiveResourceBareName(),
		"agent_name":          req.AgentName,
		"agent_names":         agentNames,
		"mcp_name":            req.MCPName,
		"resource_attributes": req.Resource.Attributes,
	}
}

// onCallGroupCodes returns the duty-group codes the caller explicitly attached.
//
// It is the historical source and stays in the input document for
// observability; the policy now reads the union from groupCodes.
func onCallGroupCodes(subj authz.Subject) []string {
	if subj.Kind != authz.SubjectKindUser {
		return nil
	}
	raw, ok := subj.Extra["oncall_group_codes"]
	if !ok || raw == "" {
		return nil
	}
	return splitCommaList(raw)
}

// groupCodes returns every group code a user subject carries, combining
// console-managed user groups with the legacy on-call duty groups.
//
// The order is sorted and deduplicated so the policy input (and the decision
// cache key derived from Extra) does not depend on map or membership order.
func groupCodes(subj authz.Subject) []string {
	if subj.Kind != authz.SubjectKindUser {
		return nil
	}
	out := make([]string, 0, 4)
	for _, key := range []string{"group_codes", "oncall_group_codes"} {
		if raw := subj.Extra[key]; raw != "" {
			out = append(out, splitCommaList(raw)...)
		}
	}
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// splitCommaList splits a comma-separated string into its non-empty elements.
func splitCommaList(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// parseRegoResult extracts the {"decision":..., "reason":...} object produced
// by the `data.oai.authz.decision` query and maps it to an authz.Decision.
func parseRegoResult(rs rego.ResultSet) (authz.Decision, string, error) {
	if len(rs) == 0 || len(rs[0].Expressions) == 0 {
		return authz.DecisionNoOpinion, "", nil
	}

	obj, ok := rs[0].Expressions[0].Value.(map[string]any)
	if !ok {
		return authz.DecisionNoOpinion, "", fmt.Errorf("opa: unexpected result shape: %T", rs[0].Expressions[0].Value)
	}

	reason, _ := obj["reason"].(string)
	decisionStr, _ := obj["decision"].(string)

	switch decisionStr {
	case "allow":
		return authz.DecisionAllow, reason, nil
	case "deny":
		return authz.DecisionDeny, reason, nil
	default:
		return authz.DecisionNoOpinion, reason, nil
	}
}

// rulesToRegoData projects []model.AuthzRuleM into the plain []map[string]any
// shape stored under data.authz.rules, matching the field names the Rego
// policy pattern-matches on.
func rulesToRegoData(rules []model.AuthzRuleM) []map[string]any {
	out := make([]map[string]any, 0, len(rules))
	for _, r := range rules {
		var resAttrs map[string]any
		if r.ResourceAttributes != "" {
			if err := json.Unmarshal([]byte(r.ResourceAttributes), &resAttrs); err != nil {
				// Malformed JSON is silently treated as "no filter" —
				// fail-safe: the rule matches attribute-wise (same as empty).
				resAttrs = nil
			}
		}
		out = append(out, map[string]any{
			"name":                r.Name,
			"subject_type":        r.SubjectType,
			"subject_id":          r.SubjectID,
			"resource_kind":       r.ResourceKind,
			"resource_name":       r.ResourceName,
			"agent_names":         r.AgentNames,
			"verbs":               r.Verbs,
			"effect":              r.Effect,
			"resource_attributes": resAttrs,
		})
	}
	return out
}
