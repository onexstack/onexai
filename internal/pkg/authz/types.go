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

// Package authz implements the multi-level (Agent / Tool / Resource)
// authorization system for oai-agent-apiserver's Agent runtime.
//
// The design mirrors k8s.io/apiserver/pkg/authorization: a small Authorizer
// interface with an Allow/Deny/NoOpinion Decision, composed via a Union
// combinator (Deny wins, otherwise any Allow wins, all-NoOpinion fails
// closed). Concrete Authorizer implementations (OPA, CMDB, Webhook, Casdoor)
// live in sibling packages; this package only defines the shared vocabulary
// and the Coordinator that wires per-level Authorizers together.
package authz

import "strings"

// SubjectKind identifies what a Subject represents. Naming is aligned with
// k8s.io/apiserver/pkg/authentication/user's User/Group/ServiceAccount
// vocabulary, so the mental model transfers directly for anyone familiar
// with Kubernetes RBAC.
type SubjectKind string

const (
	// SubjectKindUser is a single Lark user.
	SubjectKindUser SubjectKind = "user"
	// SubjectKindGroup is an OnCall duty group (may have a parent/child
	// hierarchy). Named Group (not OnCallGroup) to align with Kubernetes'
	// User/Group/ServiceAccount subject taxonomy.
	SubjectKindGroup SubjectKind = "group"
	// SubjectKindServiceAccount is a non-human internal caller (the
	// chat that signs its way in, cron jobs, etc), analogous to a
	// Kubernetes ServiceAccount.
	SubjectKindServiceAccount SubjectKind = "serviceaccount"
)

// Subject uniformly represents the principal initiating an authorization
// request: a single Lark user, an OnCall duty group, or an internal service.
type Subject struct {
	Kind SubjectKind

	// Populated when Kind == SubjectKindUser.
	UserID   string // Lark user_id.
	UserName string

	// Populated when Kind == SubjectKindGroup.
	GroupCode string // duty group code.

	// Populated when Kind == SubjectKindServiceAccount.
	ServiceName string

	// Extra carries caller-supplied attributes a policy may need beyond the
	// fields above. Authorizers may pass this through to policy input verbatim.
	Extra map[string]string
}

// PrimaryID returns the single string that best identifies this Subject for
// logging and cache keys: UserID for users, GroupCode for groups,
// ServiceName otherwise.
func (s Subject) PrimaryID() string {
	switch s.Kind {
	case SubjectKindUser:
		return s.UserID
	case SubjectKindGroup:
		return s.GroupCode
	default:
		return s.ServiceName
	}
}

// ResourceKind enumerates the addressable resource kinds. It is a string
// (not an int enum) so config/policy can introduce new kinds without a Go
// code change, while staying consistent with the "kind:name" addressing
// scheme used throughout this package.
//
// The const values below are a non-exhaustive catalog of the kinds this
// subsystem ships with. Any string value is a valid ResourceKind — CMDB
// Authorizer returns NoOpinion for kinds it doesn't handle, falling through
// to OPA/Webhook.
type ResourceKind string

const (
	ResourceKindAgent ResourceKind = "agent" // AgentM.Name.
	// ResourceKindMCP addresses an MCP Server as a whole (MCPM.Name) — distinct
	// from ResourceKindTool, which addresses one Tool exposed by an MCP Server.
	// Checked by Coordinator.AuthorizeResourceRefs when an Agent declares this
	// MCP Server in its MCPs list, i.e. before any individual Tool call is
	// ever made.
	ResourceKindMCP  ResourceKind = "mcp"  // MCPM.Name.
	ResourceKindTool ResourceKind = "tool" // A Tool name.

	ResourceKindService ResourceKind = "service" // Business service name (CMDB).
	ResourceKindCluster ResourceKind = "cluster" // EKS/K8s cluster name.

	// ResourceKindK8sNamespace addresses a K8s cluster namespace extracted from
	// Tool-call arguments (resource-level check). NOT to be confused with
	// ResourceKindNamespace, the namespace an AI resource lives in: this one is
	// a cluster object the call wants to touch.
	ResourceKindK8sNamespace ResourceKind = "k8s-namespace"

	// ResourceKindSkill addresses a Skill (model.SkillM.Name) an Agent declares
	// in its Skills list. Checked by Coordinator.AuthorizeResourceRefs at Agent
	// assembly time, before the Skill's backend is ever loaded for this request.
	ResourceKindSkill ResourceKind = "skill"

	// ResourceKindKnowledgeBase addresses a KnowledgeBase
	// (model.KnowledgeBaseM.Name) an Agent declares in its KnowledgeBases list.
	// Checked the same way as ResourceKindSkill.
	ResourceKindKnowledgeBase ResourceKind = "knowledgebase"

	// ResourceKindRole addresses a deployment role (e.g. "sre", "admin") rather
	// than an object. A service that only needs "may this person act as this
	// role" asks for it as read on role:<name>, so role membership is expressed
	// in the same rule table as everything else instead of a second list that
	// has to be kept in sync. Checked by Coordinator.AuthorizeUse.
	ResourceKindRole ResourceKind = "role"

	// ResourceKindNamespace addresses the namespace an AI resource lives in.
	// Every AI resource model (Agent/Skill/MCP/KnowledgeBase) is a namespaced
	// object; a check "use namespace:<ns>" is issued alongside the resource's
	// own check, using the same per-Agent Authorizer (see
	// Coordinator.WithNamespaceAuthz).
	ResourceKindNamespace ResourceKind = "namespace"

	// LegacyResourceKindWorkspace is the pre-rename spelling of
	// ResourceKindNamespace. It is read for one release and never written.
	LegacyResourceKindWorkspace ResourceKind = "workspace"

	// ResourceKindNone means the Tool call has no addressable resource;
	// resource-level authorization must be skipped entirely for it.
	ResourceKindNone ResourceKind = ""
)

// Resource addresses a concrete object a Tool call operates on. Name reuses
// the existing business "Name" string throughout the resource models — no new
// ID scheme is introduced.
type Resource struct {
	Kind ResourceKind
	Name string

	// Attributes carries extra key/value pairs extracted from the tool call
	// arguments (e.g. {"env": "prod"}) that a policy may need beyond Name.
	Attributes map[string]string
}

// String renders the "kind:name" addressing form used in logs and policy input.
func (r Resource) String() string {
	if r.Kind == ResourceKindNone {
		return ""
	}
	return string(r.Kind) + ":" + r.Name
}

// IsEmpty reports whether this Resource carries no addressable target, i.e.
// the Tool has no resource mapping configured for it. This is the signal
// Coordinator uses to skip resource-level authorization (NoOpinion, not
// Allow) rather than to deny it.
func (r Resource) IsEmpty() bool {
	return r.Kind == ResourceKindNone || r.Name == ""
}

// Verb expresses the authorization action, analogous to
// k8s.io/apiserver/pkg/authorization/authorizer.Attributes.GetVerb().
//
// A two-verb model following Kubernetes RBAC conventions:
//   - read: using Agents, invoking Tools, reading Resources, and accessing
//     Skill/MCP/KnowledgeBase references.
//   - write: mutating Resources and invoking mutating Tools.
//
// write does NOT imply read — each verb must be granted explicitly (least
// privilege). Admin-level access is expressed by granting both read and write
// on a resource.
type Verb string

const (
	VerbRead  Verb = "read"  // Use an Agent, invoke a Tool, read a Resource, access a Skill/MCP/KB.
	VerbWrite Verb = "write" // Mutate a Resource, invoke a mutating Tool.
)

// AuthorizationRequest is the single struct fed into every Authorizer. It
// intentionally flattens Subject/Resource so implementations (and policy
// evaluators) can pattern-match on individual fields without needing to know
// about nested struct shapes.
type AuthorizationRequest struct {
	Subject Subject
	Verb    Verb

	// AgentName is the Agent being checked for Agent-level requests. For a
	// Tool-level request in a merged multi-Agent run it is only a display name
	// (the selection joined with "."), which never matches an agent-scoped rule
	// — see EffectiveAgentNames.
	AgentName string

	// AgentNames is every Agent selected for the run. A Tool is reachable only
	// through some selected Agent, so an agent-scoped deny applies when *any*
	// selected Agent is in its scope: the deny must not be evaded by selecting
	// a second Agent whose scope the rule does not name.
	//
	// Empty means "use AgentName" (see EffectiveAgentNames), so single-Agent
	// callers need not set it.
	AgentNames []string

	ToolName string // Target Tool name; required for Tool/resource-level checks.
	MCPName  string // MCP Server owning ToolName, if known.

	Resource Resource // Resource.IsEmpty()==true means this request carries no concrete resource.
}

// EffectiveResource returns the (kind, name) addressing pair this request
// targets: Resource verbatim when non-empty (resource-level checks), or —
// when Resource is empty — synthesized from ToolName (tool-level) or
// AgentName (agent-level), in that priority order. This is the single
// implementation of "how to address an Agent/Tool/Resource-level request as
// one kind:name pair" shared by every Authorizer that needs it instead of each
// re-deriving it independently.
func (req AuthorizationRequest) EffectiveResource() (kind ResourceKind, name string) {
	if !req.Resource.IsEmpty() {
		return req.Resource.Kind, req.Resource.Name
	}
	switch {
	case req.ToolName != "":
		return ResourceKindTool, req.ToolName
	case req.AgentName != "":
		return ResourceKindAgent, req.AgentName
	default:
		return ResourceKindNone, ""
	}
}

// EffectiveResourceBareName returns the resource name without its namespace
// qualifier: for a canonical reference "ns/name" it is "name", and for a name
// that carries no slash it is that name unchanged. It is derived from
// EffectiveResource so it always agrees with the resource_name the policy sees.
//
// # Why the policy needs it
//
// Namespaced resource references are carried as canonical "namespace/name"
// entries at runtime, but a rule's resource_name historically names the object
// alone ("onexai", "onex-console", "onex-courses") — that is the contract the
// seed and OneX's reconcile path write. Matching those rules would otherwise
// fail closed on every canonical reference. A rule entry that contains a slash
// stays an exact canonical match; a rule entry without one matches the bare
// name in any namespace. The compatibility is one-directional: a bare request
// does not match a canonical rule.
//
// This deliberately does not import the agent resource model: the authz
// package stays decoupled from resource storage, and a resource name is a
// DNS1123 label that cannot contain '/', so the first slash is the separator.
func (req AuthorizationRequest) EffectiveResourceBareName() string {
	_, name := req.EffectiveResource()
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// EffectiveAgentNames returns the Agent names this request is scoped by:
// AgentNames when set, otherwise the single AgentName. It never returns an
// empty-string entry, because an empty name must not be matchable by a rule.
func (req AuthorizationRequest) EffectiveAgentNames() []string {
	if len(req.AgentNames) > 0 {
		out := make([]string, 0, len(req.AgentNames))
		for _, n := range req.AgentNames {
			if n != "" {
				out = append(out, n)
			}
		}
		return out
	}
	if req.AgentName != "" {
		return []string{req.AgentName}
	}
	return nil
}
