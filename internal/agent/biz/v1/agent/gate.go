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

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/agentauthz"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	agent "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

// authorizeRun is PEP #1: the agent-level and resource-reference checks, run
// once before a run starts (and again on resume, which is a run of its own).
//
// It is deliberately here rather than inside the engine. The engine assembles
// the agent in a goroutine whose only error channel is a failed event, so a
// denial raised there would reach the caller as "run failed" with no way to say
// "you may not use this agent". Deciding before the run starts puts the refusal
// on the request that asked for it, where it can still be a status code.
//
// # Ownership
//
// An agent's owner may use it. Ownership is stamped by the API server when the
// object is created (see handler.UserGuard), so this is the person who wrote
// the agent's prompt and chose its capabilities — not a claim by the caller.
// Without this, the console's ordinary users could create an agent and then be
// refused by a rule table that knows nothing about it. Resource references are
// still checked: owning an agent is not a grant to the skills, MCP servers and
// knowledge bases it names.
//
// A nil or disabled coordinator allows everything, so a deployment without
// authz pays nothing but a nil check.
func authorizeRun(
	ctx context.Context, coord *authz.Coordinator, resources store.AIStore,
	subj authz.Subject, agents []string, webhookAuthz *agentauthz.Resolver,
) error {
	for _, name := range agents {
		if name == "" {
			continue
		}
		// An Agent that names authorization Webhooks is decided only by them.
		// This runs even when the platform coordinator is disabled: the Agent's
		// own policy is not the platform's, and silently skipping it because
		// authz.enabled is false would widen access.
		if webhookAuthz != nil {
			authorizer, found, err := webhookAuthz.AuthorizerFor(ctx, name)
			if err != nil {
				return errno.ErrInternal.WithMessage(
					"the authorization webhook for agent %q could not be resolved: %s", name, err.Error())
			}
			if found {
				_, ruleName := sharing.SplitRef(name)
				decision, reason, err := authorizer.Authorize(ctx, authz.AuthorizationRequest{
					Subject: subj, Verb: authz.VerbRead, AgentName: ruleName,
				})
				if err != nil {
					return errno.ErrInternal.WithMessage(
						"the authorization webhook for agent %q failed", name)
				}
				if decision != authz.DecisionAllow {
					return &authz.ErrAgentAccessDenied{Subject: subj, AgentName: name, Reason: reason}
				}
				continue
			}
		}

		if coord == nil || !coord.Enabled() {
			continue
		}
		m, err := authorizeAgentGate(ctx, coord, resources, subj, name)
		if err != nil {
			return err
		}
		if m == nil {
			continue
		}
		// AuthorizeResourceRefs fails closed like every other dimension: an
		// unreferenced-by-policy Skill/MCP/KnowledgeBase is refused, so the seed
		// lists the rows an Agent needs. The three kinds are checked separately
		// so the error can name the one that was refused.
		for _, ref := range []struct {
			kind  authz.ResourceKind
			names []string
		}{
			{authz.ResourceKindSkill, m.EffectiveSkills()},
			{authz.ResourceKindMCP, m.EffectiveMCPs()},
			{authz.ResourceKindKnowledgeBase, m.EffectiveKnowledgeBases()},
		} {
			decision, reason, err := coord.AuthorizeResourceRefs(ctx, subj, name, ref.kind, ref.names)
			if err != nil {
				return errno.ErrInternal.WithMessage("the authorization backend failed for agent %q", name)
			}
			if decision == authz.DecisionDeny {
				return &authz.ErrAgentAccessDenied{Subject: subj, AgentName: name, Reason: reason}
			}
		}
	}
	return nil
}

// authorizeAgentGate checks that subj may use the named agent, and returns the
// record when it exists.
//
// It is split out of authorizeRun because a composed run checks the host agent
// here but its resource references against the composition, not against the
// host's bindings. An unknown agent still goes through policy, which fails
// closed; the caller decides whether a missing record is fatal on its surface.
func authorizeAgentGate(
	ctx context.Context, coord *authz.Coordinator, resources store.AIStore, subj authz.Subject, name string,
) (*model.AgentM, error) {
	if coord == nil || !coord.Enabled() || resources == nil || name == "" {
		return nil, nil
	}
	// name may be "namespace/name": the store lookup uses the full reference,
	// while the policy check below is keyed by the bare agent name.
	m, err := resources.Agents().Get(ctx, model.EntryFilter(name))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		// A store outage must not be read as "unknown agent": the policy check
		// below would then decide about an agent it never saw.
		return nil, storeerr.Read(err)
	}

	// The agent-level policy check is skipped for the owner; an unknown agent
	// (m == nil) still goes through policy, which fails closed.
	if err != nil || m == nil || !subjectOwnsAgent(subj, m.Owner) {
		if err := authz.AuthorizeAgentNames(ctx, coord, subj, []string{name}); err != nil {
			return nil, err
		}
	}
	if err != nil || m == nil {
		return nil, nil
	}
	return m, nil
}

// subjectOwnsAgent reports whether subj is the agent's owner.
//
// Either identity the token carries matches: rows created by the console store
// the account ID, while rows that predate account IDs store the display name.
// Only a user subject can own an agent — a service account or a duty group has
// no per-object ownership.
func subjectOwnsAgent(subj authz.Subject, owner string) bool {
	if owner == "" || subj.Kind != authz.SubjectKindUser {
		return false
	}
	return subj.UserID == owner || (subj.UserName != "" && subj.UserName == owner)
}

// subjectForRun builds the authorization subject for a run's asker. The role
// assertion travels in the request body (and so under the AK/SK signature), so
// it is trusted exactly as far as the caller's own authentication is.
//
// kind carries the caller's own claim about what it is. It matters because a
// service that calls on its own behalf and a person of the same name are not the
// same principal to a policy: the A2A surface signs as a service account, and
// collapsing it into a user would silently hand it whatever a user of that name
// was granted. An unknown kind is read as a user, which is what every caller
// before this field reported.
func subjectForRun(userID, userName, kind string) authz.Subject {
	switch kind {
	case agent.PrincipalKindService:
		return authz.Subject{Kind: authz.SubjectKindServiceAccount, ServiceName: userID}
	case agent.PrincipalKindGroup:
		return authz.Subject{Kind: authz.SubjectKindGroup, GroupCode: userID, UserName: userName}
	default:
		return authz.SubjectFromUser(userID, userName)
	}
}
