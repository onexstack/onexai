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

package authz

import (
	"context"
	"strings"
	"time"

	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

// Coordinator is the single authorization entry point consumed by the Agent
// run path (Agent-level + namespace-level, PEP #1) and the tool-call
// middleware (Tool-level + Resource-level, PEP #2). It holds two independently
// composed Authorizers — agentToolAuthz (shared by Agent-level and
// namespace-level checks, since both answer "may this Subject use this
// Agent/namespace") and resourceAuthz — process-wide singletons wired once at
// server startup (see ProvideAuthzCoordinator).
//
// The ResourceExtractor is deliberately NOT stored on Coordinator: unlike the
// Authorizers, which Tool maps to which Resource depends on which MCP Servers
// are loaded for a *specific* Runner (see MCPM.ResourceMappings), so
// AuthorizeToolCall takes it as a per-call parameter instead — built once per
// Runner assembly from that Runner's resolved MCP servers, not once per
// process.
//
// Both AuthorizeAgent and AuthorizeToolCall delegate their combination logic
// to evaluateLevels: each participating dimension (agent, namespace, tool,
// resource) is a leveledCheck; any Deny ends the request as Deny, otherwise
// the request is allowed only if at least one participating dimension
// returned Allow — all-NoOpinion still fails closed.
type Coordinator struct {
	agentToolAuthz Authorizer // backs Agent-level, namespace-level, and Tool-level checks
	resourceAuthz  Authorizer // backs Resource-level checks

	enabled bool // AuthzOptions.Enabled — false makes the Coordinator a no-op (always Allow).

	// registry and agentAuthzBackendsLookup, when both non-nil, let
	// AuthorizeAgent resolve a per-Agent subset of Authorizers (via
	// AgentM.AuthzBackends) instead of always using the process-wide
	// agentAuthz Union. Set via WithAgentBackendSelection; left nil this
	// preserves the default behavior exactly — every Agent uses agentAuthz.
	registry                 *Registry
	agentAuthzBackendsLookup func(agentName string) ([]string, bool)

	// namespaceLookup, when non-nil, enables namespace-level (business-line)
	// authorization (see ResourceKindNamespace's doc). It resolves a (kind,
	// name) resource to the namespace that resource belongs to; the returned
	// namespace is then checked as its own resource. Set via WithNamespaceAuthz;
	// left nil this preserves the default behavior exactly — no namespace check
	// is performed.
	namespaceLookup func(kind ResourceKind, name string) (string, bool)
}

// NewCoordinator wires the two per-level Authorizers.
//
// agentToolAuthz backs Agent-level, namespace-level, and Tool-level checks.
// This is the common case — most deployments use the same backends for both
// "may this Subject use this Agent?" and "may this Subject invoke this Tool?".
// If future requirements need independent backends per level, add a
// WithToolAuthz override method rather than adding a third constructor
// parameter.
func NewCoordinator(agentToolAuthz, resourceAuthz Authorizer, enabled bool) *Coordinator {
	return &Coordinator{
		agentToolAuthz: agentToolAuthz,
		resourceAuthz:  resourceAuthz,
		enabled:        enabled,
	}
}

// WithAgentBackendSelection enables per-Agent authz backend selection for
// AuthorizeAgent. registry must already have every backend name
// AgentM.AuthzBackends might reference Register()'d (see
// ProvideAuthzCoordinator). lookup resolves an agentName to its parsed
// AuthzBackends list — returning (nil, false) means "no override for this
// agent", falling back to the process-wide agentAuthz. Keeping this lookup as
// a plain function (rather than Coordinator depending on the storage layer
// directly) keeps the authz package decoupled from resource storage.
//
// This only affects AuthorizeAgent (Agent-level + namespace-level, PEP #1).
// AuthorizeToolCall (Tool/Resource-level, PEP #2) is unaffected: by the time a
// Tool call happens, agentName there is the merged multi-agent config name
// (e.g. "agent-a.agent-b"), not a single AgentM.Name, so per-Agent backend
// selection has no well-defined meaning at that level.
func (c *Coordinator) WithAgentBackendSelection(registry *Registry, lookup func(agentName string) ([]string, bool)) *Coordinator {
	c.registry = registry
	c.agentAuthzBackendsLookup = lookup
	return c
}

// WithNamespaceAuthz enables namespace-level (business-line) authorization (see
// ResourceKindNamespace's doc). lookup resolves a (kind, name) resource to the
// namespace it belongs to; returning (_, false) — or an empty namespace —
// skips the namespace check entirely for that resource.
//
// Once set, a "use namespace:<ns>" check is issued:
//   - by AuthorizeAgent, ahead of the "use agent:<name>" check, for the Agent's
//     own namespace;
//   - by AuthorizeResourceRefs, alongside each Skill/MCP/KnowledgeBase
//     reference the Agent declares.
//
// Every such check uses the same per-Agent Authorizer as the corresponding
// resource-level check (see resolveAgentAuthorizer).
func (c *Coordinator) WithNamespaceAuthz(lookup func(kind ResourceKind, name string) (string, bool)) *Coordinator {
	c.namespaceLookup = lookup
	return c
}

// namespaceFor resolves the namespace of the named resource, or ("", false)
// when no lookup is configured or the resource has none.
func (c *Coordinator) namespaceFor(kind ResourceKind, name string) (string, bool) {
	if c == nil || c.namespaceLookup == nil || name == "" {
		return "", false
	}
	return c.namespaceLookup(kind, name)
}

// Enabled reports whether this Coordinator actively enforces authorization.
func (c *Coordinator) Enabled() bool { return c != nil && c.enabled }

// AgentBackendNames returns the backend names an Agent's AuthzBackends may
// reference, sorted.
//
// A nil coordinator (authorization disabled) or a coordinator without the
// per-Agent registry answers an empty slice: the console still offers the
// "none" opt-out, which needs no registered backend.
func (c *Coordinator) AgentBackendNames() []string {
	if c == nil || c.registry == nil {
		return []string{}
	}
	return c.registry.Names()
}

// resolveAgentAuthorizer returns the Authorizer that should evaluate
// Agent-level, namespace-level, and Resource-ref (Skill/MCP/KnowledgeBase)
// requests for agentRef: either the process-wide agentAuthz Union, or — when
// WithAgentBackendSelection is configured and this Agent declares a non-empty
// AuthzBackends list — the per-Agent subset resolved via Registry.Resolve.
//
// agentRef may be "namespace/name": the store lookup needs the full reference,
// while the registry (and the rules it evaluates) is keyed by the bare name.
func (c *Coordinator) resolveAgentAuthorizer(agentRef string) Authorizer {
	authorizer := c.agentToolAuthz
	if c.registry != nil && c.agentAuthzBackendsLookup != nil {
		if backends, ok := c.agentAuthzBackendsLookup(agentRef); ok && len(backends) > 0 {
			_, ruleName := sharing.SplitRef(agentRef)
			authorizer = c.registry.Resolve(ruleName, backends)
		}
	}
	return authorizer
}

// AuthorizeAgent implements PEP #1 (Agent-level + namespace-level
// authorization), evaluated before a Runner is built for the given agentName.
func (c *Coordinator) AuthorizeAgent(ctx context.Context, subj Subject, agentRef string) (Decision, string, error) {
	if c == nil || !c.enabled {
		return DecisionAllow, "authz disabled", nil
	}
	// A qualified reference names its namespace directly; a bare one is
	// resolved through the namespace lookup. Rules are always keyed by the
	// bare agent name, so the request carries that, not the qualified string.
	namespace, ruleName := sharing.SplitRef(agentRef)
	authorizer := c.resolveAgentAuthorizer(agentRef)

	// Namespace-level check goes first — it's the coarse-grained gate
	// ("does this Subject belong to this business line?"). If the Subject has
	// no access to the namespace at all, there is no need to evaluate
	// individual Agents within it.
	checks := make([]leveledCheck, 0, 2)
	if namespace == "" {
		if looked, ok := c.namespaceFor(ResourceKindAgent, ruleName); ok {
			namespace = looked
		}
	}
	if namespace != "" {
		checks = append(checks, leveledCheck{
			name:       "namespace",
			authorizer: authorizer,
			req: AuthorizationRequest{
				Subject: subj, Verb: VerbRead, AgentName: ruleName,
				Resource: Resource{Kind: ResourceKindNamespace, Name: namespace},
			},
		})
	}
	checks = append(checks, leveledCheck{
		name:       "agent",
		authorizer: authorizer,
		req:        AuthorizationRequest{Subject: subj, Verb: VerbRead, AgentName: ruleName},
	})
	return evaluateLevels(ctx, DecisionDeny, checks...)
}

// AuthorizeResourceRefs checks subj against every name in names as an
// independent "use" request of the given kind (ResourceKindSkill,
// ResourceKindMCP, or ResourceKindKnowledgeBase) — the Skill/MCP/KnowledgeBase
// resources an Agent declares in its Skills/MCPs/KnowledgeBases lists.
// Evaluated at Agent-assembly time, once per declared resource, using the same
// per-Agent Authorizer selection as AuthorizeAgent (see
// resolveAgentAuthorizer/WithAgentBackendSelection).
//
// All-NoOpinion fails closed, exactly like AuthorizeAgent/AuthorizeToolCall: a
// reference with no rule is refused. There used to be a documented "opt-out"
// default here (allow unless a rule denies), but it could never take effect —
// the Union folds all-NoOpinion to Deny before the dimension-level default is
// applied — so it only served to make the seed's exhaustiveness load-bearing in
// a non-obvious way. hack/authz-rules-seed.sql lists the rows an Agent needs.
func (c *Coordinator) AuthorizeResourceRefs(ctx context.Context, subj Subject, agentRef string, kind ResourceKind, names []string) (Decision, string, error) {
	if c == nil || !c.enabled || len(names) == 0 {
		return DecisionAllow, "authz disabled or no resources to check", nil
	}
	authorizer := c.resolveAgentAuthorizer(agentRef)
	_, ruleName := sharing.SplitRef(agentRef)

	var checks []leveledCheck
	for _, name := range names {
		if name == "" {
			continue
		}
		// The resource's namespace, when it declares one, is checked first: it
		// is the coarse-grained gate ("does this Subject belong to this
		// business line?"), so there is no point evaluating the resource
		// itself if the namespace is refused.
		if ns, ok := c.namespaceFor(kind, name); ok && ns != "" {
			checks = append(checks, leveledCheck{
				name:       "namespace:" + ns,
				authorizer: authorizer,
				req: AuthorizationRequest{
					Subject: subj, Verb: VerbRead, AgentName: ruleName,
					Resource: Resource{Kind: ResourceKindNamespace, Name: ns},
				},
			})
		}
		checks = append(checks, leveledCheck{
			name:       string(kind) + ":" + name,
			authorizer: authorizer,
			req: AuthorizationRequest{
				Subject: subj, Verb: VerbRead, AgentName: ruleName,
				Resource: Resource{Kind: kind, Name: name},
			},
		})
	}
	if len(checks) == 0 {
		return DecisionAllow, "no resources to check", nil
	}
	return evaluateLevels(ctx, DecisionDeny, checks...)
}

// AuthorizeToolCall implements PEP #2 (Tool-level + Resource-level
// authorization), evaluated immediately before a real Tool invocation.
// argumentsInJSON is the raw tool-call payload, used only for resource
// extraction — it is never mutated. extractor resolves argumentsInJSON into a
// Resource; pass NoopResourceExtractor{} (or a nil-safe equivalent) when this
// Runner has no resource mappings configured.
//
// Verb handling: the Tool-level check always uses VerbRead (invoking any Tool
// is a "read" action on the Tool itself), while the Resource-level check uses
// the verb extracted from the resource mapping (defaults to VerbRead, but
// mutating tools declare VerbWrite explicitly). This means a single Tool
// invocation may produce two checks with different verbs — one for the Tool,
// one for the Resource it targets.
func (c *Coordinator) AuthorizeToolCall(
	ctx context.Context,
	subj Subject,
	agentNames []string,
	toolName,
	mcpName,
	argumentsInJSON string,
	extractor ResourceExtractor,
) (Decision, string, error) {
	if c == nil || !c.enabled {
		return DecisionAllow, "authz disabled", nil
	}
	if extractor == nil {
		extractor = NoopResourceExtractor{}
	}

	// agentName is the display name only; the names are what agent-scoped rules
	// are matched against, so a merged run cannot escape a rule about one of its
	// Agents. A caller may name an Agent as "namespace/name": the rules are
	// keyed by the bare name, so the qualifier is dropped here.
	ruleNames := make([]string, 0, len(agentNames))
	for _, agentRef := range agentNames {
		if _, name := sharing.SplitRef(agentRef); name != "" {
			ruleNames = append(ruleNames, name)
		}
	}
	agentName := strings.Join(ruleNames, ".")

	checks := []leveledCheck{
		{
			name:       "tool",
			authorizer: c.agentToolAuthz,
			req: AuthorizationRequest{
				Subject: subj, Verb: VerbRead, AgentName: agentName, AgentNames: ruleNames,
				ToolName: toolName, MCPName: mcpName,
			},
		},
	}

	resource, verb, extractErr := extractor.Extract(ctx, mcpName, toolName, argumentsInJSON)
	if extractErr != nil {
		// Extraction failures are treated as "no resource mapping" (not an
		// authorization failure) — a malformed/unexpected argument shape
		// should not cause a false-positive Deny.
		resource = Resource{Kind: ResourceKindNone}
	}
	if !resource.IsEmpty() {
		checks = append(checks, leveledCheck{
			name:       "resource",
			authorizer: c.resourceAuthz,
			req: AuthorizationRequest{
				Subject: subj, Verb: verb, AgentName: agentName, AgentNames: ruleNames,
				ToolName: toolName, MCPName: mcpName,
				Resource: resource,
			},
		})
	}
	return evaluateLevels(ctx, DecisionDeny, checks...)
}

// AuthorizeUse answers "may any of these subjects perform verb on kind:name?"
// — the entry point for a caller whose identity is a set rather than one
// subject (a signed-in user plus the groups they belong to, which is how
// Kubernetes models it: one request carries the user and its group list).
//
// Each subject is one check, combined with the package's usual algebra: any Deny
// ends the request, otherwise one Allow is enough, and all-NoOpinion fails
// closed. Deny winning across subjects is deliberate — a group the caller
// belongs to may be denied something an individual grant allows, and the
// refusal must not depend on which subject happened to be checked first.
//
// verb and the resource are the caller's to choose; the only shipped use is a
// service asking about a role (ResourceKindRole, VerbRead), which is what lets
// a deployment express role membership as rules instead of a second list.
func (c *Coordinator) AuthorizeUse(ctx context.Context, subjects []Subject, verb Verb, kind ResourceKind, name string) (Decision, string, error) {
	return c.AuthorizeUseWithDefault(ctx, subjects, verb, kind, name, DecisionDeny)
}

// AuthorizeUseWithDefault is AuthorizeUse with a caller-chosen answer for
// NoOpinion.
//
// The run path wants Deny: a grant that no rule names is not a grant. The
// console's resource guard is additive during rollout and needs to tell "a rule
// said no" from "no rule spoke", because the second case keeps the legacy
// owner/shared behaviour. Collapsing both into Deny would either lock everyone
// out or require guessing from a reason string.
func (c *Coordinator) AuthorizeUseWithDefault(
	ctx context.Context, subjects []Subject, verb Verb, kind ResourceKind, name string, onNoOpinion Decision,
) (Decision, string, error) {
	return c.AuthorizeScoped(ctx, subjects, verb, kind, name, nil, nil, onNoOpinion)
}

// AuthorizeScoped is AuthorizeUseWithDefault with an Agent scope and resource
// attributes.
//
// agentNames, when non-empty, is what agent-scoped AuthorizationRule.agentNames
// match against; the joined name is also set as AgentName so the policy input
// carries a display name. attributes are the resource attributes a policy may
// match (for example env=prod), which Kubernetes carries in a SubjectAccessReview's
// extra map. It is the bridge the SubjectAccessReview endpoint uses: Kubernetes
// has no notion of an Agent, so the review carries it in its extra map and the
// handler passes it here.
func (c *Coordinator) AuthorizeScoped(
	ctx context.Context, subjects []Subject, verb Verb, kind ResourceKind, name string,
	agentNames []string, attributes map[string]string, onNoOpinion Decision,
) (Decision, string, error) {
	if c == nil || !c.enabled {
		return DecisionAllow, "authz disabled", nil
	}
	if len(subjects) == 0 {
		// No identity is not "nobody objects" — it is a caller that could not
		// say who it is, and the safe reading is the same as an empty rule set.
		return DecisionDeny, "authorization: no subject to authorize", nil
	}

	agentName := strings.Join(agentNames, ".")
	checks := make([]leveledCheck, 0, len(subjects))
	for _, subj := range subjects {
		checks = append(checks, leveledCheck{
			name:       string(kind),
			authorizer: c.agentToolAuthz,
			req: AuthorizationRequest{
				Subject:    subj,
				Verb:       verb,
				AgentName:  agentName,
				AgentNames: agentNames,
				Resource:   Resource{Kind: kind, Name: name, Attributes: attributes},
			},
		})
	}
	return evaluateLevels(ctx, onNoOpinion, checks...)
}

// leveledCheck is one dimension participating in a multi-level authorization
// decision (e.g. "agent", "namespace", "tool", "resource"). Each dimension
// that is included in the checks slice passed to evaluateLevels always
// participates — omit a dimension from the slice (rather than adding an
// "optional" flag) when it should not participate for this call, as
// AuthorizeToolCall does for "resource" when the Tool has no resource mapping.
type leveledCheck struct {
	name       string // for reason strings, e.g. "tool", "resource", "namespace"
	authorizer Authorizer
	req        AuthorizationRequest
}

// decisionResult is one authorizer's answer collected during a multi-result
// evaluation (see combineResults). It carries the source (authorizer name or
// dimension name), the decision, and any execution error.
type decisionResult struct {
	source   string
	decision Decision
	reason   string
	err      error
}

// evaluateLevels applies the same Deny-wins / any-Allow-passes combination
// semantics as unionAuthorizer.Authorize (see union.go), but across an ordered
// list of *dimensions* (this request's agent/namespace/tool/resource checks)
// rather than across multiple Authorizer instances answering the same request.
// Short-circuits on the first Deny or the first Authorizer execution error.
//
// An Authorizer execution error is always fail-closed: it ends the request as
// Deny and is propagated, so callers can tell "authz engine is down" from a
// business Deny. The run path uses DecisionDeny as defaultOnNoOpinion; the
// console resource guard uses DecisionNoOpinion so additive mode can keep the
// legacy owner/shared behaviour. NoOpinion is only ever returned when the caller
// explicitly asks for it.
func evaluateLevels(ctx context.Context, defaultOnNoOpinion Decision, checks ...leveledCheck) (Decision, string, error) {
	var results []decisionResult
	for _, c := range checks {
		start := time.Now()
		decision, reason, err := c.authorizer.Authorize(ctx, c.req)
		elapsed := time.Since(start).Seconds()

		// Record authz metrics (nil-safe: M is only set once authz is wired).
		resourceKind, _ := c.req.EffectiveResource()
		dec := "no_opinion"
		switch decision {
		case DecisionAllow:
			dec = "allow"
		case DecisionDeny:
			dec = "deny"
		}
		if err != nil {
			dec = "error"
		}
		M.observeCheck(c.name, dec, string(resourceKind), string(c.req.Verb), c.authorizer.Name(), elapsed)
		if decision == DecisionDeny {
			M.observeDeny(c.name, string(resourceKind), string(c.req.Verb), c.authorizer.Name(), string(c.req.Subject.Kind))
		}

		results = append(results, decisionResult{
			source: c.name, decision: decision, reason: reason, err: err,
		})

		// Short-circuit on Deny: a single Deny ends the request regardless of
		// what remaining checks would return. Skip them to avoid wasted work
		// (e.g. an expensive CMDB query when tool-level already denied).
		if decision == DecisionDeny {
			break
		}
		// Short-circuit on execution error: the result was already collected
		// above and combineResults maps it to Deny, so the remaining checks are
		// wasted work.
		if err != nil {
			break
		}
	}

	// Execution errors map to Deny and are propagated, so callers can
	// distinguish "authz engine is down" from "business Deny". There is no
	// fail-open mode: a backend outage must not grant access.
	errorHandler := func(_ string, err error) error {
		return err
	}
	return combineResults(results, defaultOnNoOpinion, errorHandler)
}

// combineResults applies Deny-wins / any-Allow-passes semantics across results.
// defaultDecision is returned when every result is NoOpinion.
//
// onError maps an execution error to one of exactly two outcomes: nil (treat it
// as NoOpinion and keep evaluating — unionAuthorizer's choice, so one broken
// backend cannot block the others) or a non-nil error (abort as Deny —
// evaluateLevels' choice, so an outage fails closed). Treating an error as a
// grant is deliberately not expressible.
func combineResults(results []decisionResult, defaultDecision Decision, onError func(source string, err error) error) (Decision, string, error) {
	sawAllow := false
	var reasons []string
	for _, r := range results {
		if r.err != nil {
			if e := onError(r.source, r.err); e != nil {
				return DecisionDeny, "", e
			}
			continue
		}
		if r.decision == DecisionDeny {
			return DecisionDeny, joinReason(r.source, r.reason), nil
		}
		if r.decision == DecisionAllow {
			sawAllow = true
			reasons = append(reasons, joinReason(r.source, r.reason))
		}
	}
	if sawAllow {
		return DecisionAllow, strings.Join(reasons, "; "), nil
	}
	switch defaultDecision {
	case DecisionAllow:
		return DecisionAllow, "all NoOpinion (default allow)", nil
	case DecisionNoOpinion:
		// The caller asked to distinguish "no rule spoke" from a refusal. The
		// console's resource guard uses this to keep the legacy owner/shared
		// behaviour in additive mode.
		return DecisionNoOpinion, "all NoOpinion", nil
	default:
		return DecisionDeny, "authorization: all levels NoOpinion (fail-closed)", nil
	}
}
