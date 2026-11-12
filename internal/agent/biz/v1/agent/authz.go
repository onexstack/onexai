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
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk"

	"github.com/onexstack/onexai/internal/agent/pkg/agentauthz"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

// authzGate evaluates one run's tool calls against the authorization
// Coordinator (PEP #2/#3: tool-level + resource-level). It is built per run by
// the engine, because the tool -> MCP attribution and the resource mappings it
// needs come from the MCP servers that run selected.
//
// Its result is not a refusal: a call authorization does not allow is handed to
// the approval handler as "this needs a human", which is how a denial becomes
// an elevation request rather than a dead end. See approvalHandler.needsApproval.
type authzGate struct {
	coordinator *authz.Coordinator
	// webhookAuthz resolves the selected Agents' own authorization Webhooks.
	// When any of them is configured, those hooks are the only authority for
	// this run's tool calls.
	webhookAuthz *agentauthz.Resolver
	// agentNames is the run's whole selection. An agent-scoped deny applies when
	// any of them is in its scope (see authz.AuthorizationRequest.AgentNames):
	// selecting a second Agent must not be a way around a rule about the first.
	agentNames []string
	// toolToMCP maps a model-facing tool name to the MCP server that published
	// it. A built-in tool has no entry, i.e. no MCP attribution.
	toolToMCP map[string]string
	// extractor turns a tool call's arguments into the Resource the
	// resource-level check addresses. Built once per run from the selected MCP
	// servers' ResourceMappings.
	extractor authz.ResourceExtractor
	// reserved are the tool names authorization must never gate, whatever a
	// rule says — the same set approval excludes, so the tool whose contract is
	// to consult a human cannot be gated on consulting a human.
	reserved map[string]bool
}

// newAuthzGate builds a run's gate. A nil coordinator (authorization disabled
// at the process level) yields nil, and approvalHandler treats a nil gate as
// "policy only".
func newAuthzGate(
	coordinator *authz.Coordinator, webhookAuthz *agentauthz.Resolver, agentNames []string,
	toolToMCP map[string]string, resourceMappings map[string]map[string]authz.ToolResourceMapping,
	reserved []string,
) *authzGate {
	// A disabled coordinator still yields a gate when an Agent names its own
	// authorization Webhooks: that policy is independent of authz.enabled.
	if (coordinator == nil || !coordinator.Enabled()) && webhookAuthz == nil {
		return nil
	}
	if toolToMCP == nil {
		toolToMCP = map[string]string{}
	}
	res := make(map[string]bool, len(reserved))
	for _, name := range reserved {
		res[name] = true
	}
	return &authzGate{
		coordinator:  coordinator,
		webhookAuthz: webhookAuthz,
		agentNames:   agentNames,
		toolToMCP:    toolToMCP,
		extractor:    authz.NewMCPResourceExtractor(resourceMappings),
		reserved:     res,
	}
}

// authorize reports whether the run's caller may invoke this tool call.
//
// allowed=false is not a refusal — the caller (approvalHandler) turns it into a
// human approval request, carrying reason onto the card. err means authorization
// could not answer at all, which is also fail-closed: the call is held for a
// human rather than run on the assumption that an unreachable engine is a
// permissive one.
func (g *authzGate) authorize(ctx context.Context, tCtx *adk.ToolContext, argumentsInJSON string) (allowed bool, reason string, err error) {
	if g == nil {
		return true, "", nil
	}
	name := toolNameOf(tCtx)
	if g.reserved[name] {
		return true, "", nil
	}

	subj, ok := authz.SubjectFromContext(ctx)
	if !ok {
		// A run that reached the tool node without an identity is a wiring bug,
		// not an unauthenticated caller: the agent biz always attaches one. Fail
		// closed so the bug surfaces as a question to a human rather than as an
		// unexplained grant.
		return false, "", fmt.Errorf("authz: no subject on the run context for tool %q", name)
	}

	// An Agent that names authorization Webhooks is decided by them alone.
	if g.webhookAuthz != nil {
		allowed, found, reason, err := g.authorizeWebhooks(ctx, subj, name)
		if err != nil {
			return false, reason, err
		}
		if found {
			return allowed, reason, nil
		}
	}
	if g.coordinator == nil || !g.coordinator.Enabled() {
		return true, "", nil
	}

	decision, reason, err := g.coordinator.AuthorizeToolCall(
		ctx, subj, g.agentNames, name, g.toolToMCP[name], argumentsInJSON, g.extractor)
	if err != nil {
		return false, reason, err
	}
	return decision == authz.DecisionAllow, reason, nil
}

// authorizeWebhooks evaluates the tool call against the selected Agents' own
// authorization Webhooks.
//
// found=false means none of the selected Agents names one, so the caller runs
// the platform path. When at least one does, the union of those hooks decides;
// the platform coordinator is deliberately not consulted, matching the
// requirement that an Agent-level policy replaces the platform's.
func (g *authzGate) authorizeWebhooks(
	ctx context.Context, subj authz.Subject, toolName string,
) (allowed bool, found bool, reason string, err error) {
	var authorizers []authz.Authorizer
	ruleNames := make([]string, 0, len(g.agentNames))
	for _, ref := range g.agentNames {
		authorizer, ok, resolveErr := g.webhookAuthz.AuthorizerFor(ctx, ref)
		if resolveErr != nil {
			return false, true, "", resolveErr
		}
		if !ok {
			continue
		}
		authorizers = append(authorizers, authorizer)
		if _, name := sharing.SplitRef(ref); name != "" {
			ruleNames = append(ruleNames, name)
		}
	}
	if len(authorizers) == 0 {
		return false, false, "", nil
	}

	union := authz.NewUnionAuthorizer(authorizers...)
	decision, reason, err := union.Authorize(ctx, authz.AuthorizationRequest{
		Subject: subj, Verb: authz.VerbRead,
		AgentName: strings.Join(ruleNames, "."), AgentNames: ruleNames,
		ToolName: toolName, MCPName: g.toolToMCP[toolName],
	})
	if err != nil {
		return false, true, reason, err
	}
	return decision == authz.DecisionAllow, true, reason, nil
}

// toolOriginsFor resolves the tool -> MCP attribution for one run, tolerating a
// nil lookup (a deployment with no MCP manager).
func (e *einoEngine) toolOriginsFor(mcps, agents []string) map[string]string {
	if e.toolOrigins == nil {
		return nil
	}
	return e.toolOrigins(mcps, agents)
}

// authzResourceMappings adapts a store lookup into the per-run map
// BuildResourceMappingIndex consumes: MCP server name -> its raw
// resource_mappings JSON. Servers with no mapping configured are omitted.
func authzResourceMappings(lookup func(mcps []string) map[string]string, mcps []string) map[string]map[string]authz.ToolResourceMapping {
	if lookup == nil {
		return nil
	}
	index, errs := authz.BuildResourceMappingIndex(lookup(mcps))
	for name, err := range errs {
		slog.Warn("authz: ignoring malformed resource_mappings", "mcp", name, "error", err)
	}
	return index
}
