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

// Package sharing is the one implementation of the cross-namespace rule.
//
// A capability resource (MCP server, Skill, knowledge base, Prompt, Memory) is
// owned by a namespace. An agent in another namespace may use it only when the
// owner says so, and the owner says so in the resource's own spec. That is the
// same handshake Gateway API models with ReferenceGrant, folded into the object
// because here the owner of the resource is also the grantor.
//
// The rule lives here rather than in each consumer because admission, the
// controllers and the run path all have to agree. A divergence between them is
// not a cosmetic bug: admission would admit a reference the run then refuses,
// or worse, the run would accept one admission rejected.
package sharing

import "strings"

// GroupName is the API group shared by the resources in this project.
const GroupName = "agent.onexai.io"

// AllowAll opens a resource to every namespace.
const AllowAll = "*"

// Kind names the referrer and the capability resources a grant can cover.
// They are constants rather than free strings so validation, the controllers
// and the run path cannot drift apart on spelling.
const (
	// KindAgent is the only referrer kind a grant can name. Every
	// cross-namespace capability is ultimately used by an Agent, whether the
	// reference lives in the Agent spec or in a binding.
	KindAgent = "Agent"

	KindMCPServer     = "MCPServer"
	KindSkill         = "Skill"
	KindKnowledgeBase = "KnowledgeBase"
	KindPrompt        = "Prompt"
	KindMemory        = "Memory"
	KindModel         = "Model"
	KindCustomCommand = "CustomCommand"
)

// shareableKinds is the single table of kinds an owner may open to another
// namespace with a ReferenceGrant. Everything not listed is same-namespace by
// construction: a custom command runs with the caller's authority, and agent
// templates, delegates, calls, runtime profiles and webhooks are addressed in
// one namespace by the controller and the runtime.
var shareableKinds = map[string]struct{}{
	KindMCPServer:     {},
	KindSkill:         {},
	KindKnowledgeBase: {},
	KindPrompt:        {},
	KindMemory:        {},
	KindModel:         {},
}

// Shareable reports whether a cross-namespace reference to this kind may be
// admitted by a ReferenceGrant. A kind absent from the table is same-namespace
// only; refusing is the safe answer for a newly added kind.
func Shareable(kind string) bool {
	_, ok := shareableKinds[kind]
	return ok
}

// SplitRef parses a reference entry. A bare entry (no slash) has an empty
// namespace; the caller supplies its own default. Resource names are DNS
// labels and never contain a slash, so the first slash is the separator.
func SplitRef(entry string) (namespace, name string) {
	if i := strings.IndexByte(entry, '/'); i >= 0 {
		return entry[:i], entry[i+1:]
	}
	return "", entry
}

// Resolve parses entry against referrerNamespace: a bare name belongs to the
// referrer, a qualified entry names its own namespace.
func Resolve(referrerNamespace, entry string) Ref {
	namespace, name := SplitRef(entry)
	return Ref{Namespace: Normalize(namespaceOrDefault(namespace, referrerNamespace)), Name: name}
}

// ResolveKind is Resolve with the group and target kind filled in, which is
// the shape a grant decision takes.
func ResolveKind(referrerNamespace, kind, entry string) Ref {
	ref := Resolve(referrerNamespace, entry)
	ref.Group = GroupName
	ref.Kind = kind
	return ref
}

// Canonical renders the storage spelling, "namespace/name". An empty name
// yields the empty string, which is how "no reference" is stored.
func Canonical(namespace, name string) string {
	if name == "" {
		return ""
	}
	return Normalize(namespace) + "/" + name
}

// CanonicalEntry resolves one entry and renders it as "namespace/name".
func CanonicalEntry(referrerNamespace, entry string) string {
	namespace, name := SplitRef(entry)
	return Canonical(namespaceOrDefault(namespace, referrerNamespace), name)
}

func namespaceOrDefault(namespace, fallback string) string {
	if namespace != "" {
		return namespace
	}
	return fallback
}

// DefaultNamespace is the namespace an empty namespace resolves to. It mirrors
// the historical default so records without a namespace keep resolving.
const DefaultNamespace = "helper-public"

// Normalize returns namespace unchanged, or DefaultNamespace when it is empty.
func Normalize(namespace string) string {
	if namespace == "" {
		return DefaultNamespace
	}
	return namespace
}

// Ref identifies one object in an authorization decision.
type Ref struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

// Grant is one ReferenceGrant rule flattened for evaluation. Namespace is the
// grant's namespace, which is also the referenced object's namespace.
//
// From deliberately has no name: a grant answers the namespace-level question
// "may this tenant's agents reach my object at all?". Which concrete agents
// receive the capability is decided by AgentBinding / ClusterAgentBinding.
type Grant struct {
	Namespace     string
	FromGroup     string
	FromKind      string
	FromNamespace string
	ToGroup       string
	ToKind        string
	// ToName empty means every object of ToKind in Namespace.
	ToName string
}

// AllowsGrant reports whether from may reference to.
//
// A reference never crosses a namespace boundary without a matching grant.
// Same-namespace references are always allowed and need no grant, which is the
// Kubernetes rule and the reason ReferenceGrant carries only cross-namespace
// permissions.
func AllowsGrant(grants []Grant, from, to Ref) bool {
	if Normalize(from.Namespace) == Normalize(to.Namespace) {
		return true
	}
	for _, grant := range grants {
		if grantAllows(grant, from, to) {
			return true
		}
	}
	return false
}

// GrantIndex is the in-memory form of the ReferenceGrant set, keyed by the
// target namespace. Building it once per reconcile keeps grant evaluation out
// of the per-reference path.
type GrantIndex struct {
	byNamespace map[string][]Grant
}

// NewGrantIndex groups grants by the namespace they live in.
func NewGrantIndex(grants []Grant) *GrantIndex {
	index := &GrantIndex{byNamespace: make(map[string][]Grant, len(grants))}
	for _, grant := range grants {
		ns := Normalize(grant.Namespace)
		index.byNamespace[ns] = append(index.byNamespace[ns], grant)
	}
	return index
}

// Allows reports whether from may reference to. A nil index still permits
// same-namespace references, which are the only ones that need no grant.
func (i *GrantIndex) Allows(from, to Ref) bool {
	if Normalize(from.Namespace) == Normalize(to.Namespace) {
		return true
	}
	if i == nil {
		return false
	}
	return AllowsGrant(i.byNamespace[Normalize(to.Namespace)], from, to)
}

// AllowsAll reports whether every referrer namespace may use the target.
//
// A run unions the capabilities of every selected agent, so a resource one
// referrer may use becomes available to all of them. Checking "any referrer"
// would let one tenant's grant widen another tenant's run; the answer must be
// yes for every namespace that takes part.
func (i *GrantIndex) AllowsAll(referrerNamespaces []string, to Ref) bool {
	if len(referrerNamespaces) == 0 {
		return false
	}
	for _, namespace := range referrerNamespaces {
		if !i.Allows(Ref{Group: GroupName, Kind: KindAgent, Namespace: namespace}, to) {
			return false
		}
	}
	return true
}

func grantAllows(grant Grant, from, to Ref) bool {
	if Normalize(grant.Namespace) != Normalize(to.Namespace) {
		return false
	}
	if grant.FromGroup != from.Group || grant.FromKind != from.Kind {
		return false
	}
	if Normalize(grant.FromNamespace) != Normalize(from.Namespace) {
		return false
	}
	if grant.ToGroup != to.Group || grant.ToKind != to.Kind {
		return false
	}
	return grant.ToName == "" || grant.ToName == to.Name
}
