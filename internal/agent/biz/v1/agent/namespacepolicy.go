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
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
)

// referenceTarget is one capability a run is about to use, already resolved to
// a canonical namespace/name entry.
type referenceTarget struct {
	kind  string
	label string
	entry string
}

// checkReferencePolicies is the one namespace gate for every run.
//
// A reference never crosses a namespace boundary without a matching
// ReferenceGrant, except for the kinds sharing marks unshareable: those must
// stay in the referrer's namespace. The rule lives here, above the store, so a
// composed run and an ordinary agent run cannot disagree.
//
// # Why every referrer must admit the target
//
// A run unions the capabilities of every selected agent. A resource one
// referrer may use therefore becomes available to all of them, so admitting it
// when *any* referrer has a grant would let one tenant's grant widen another
// tenant's run. The answer has to be yes for every namespace taking part.
//
// # Canonical entries only
//
// Every entry reaching this function has been normalized by the caller. A bare
// entry is treated as belonging to the first referrer, which keeps a legacy
// row working; it is never resolved by scanning the cluster for a same-named
// object in another namespace.
func (r Resolver) checkReferencePolicies(
	ctx context.Context, records []*model.AgentM, referrers []string, spec RunSpec, composed bool,
) error {
	if r.Resources == nil || len(referrers) == 0 {
		return nil
	}
	referrers = normalizeReferrers(referrers)
	index, err := r.grantIndex(ctx)
	if err != nil {
		return err
	}
	admin, err := r.adminTargets(ctx, records)
	if err != nil {
		return err
	}

	// A grant created a moment ago may not have reached the informer yet. The
	// refusal path gets one live read so a persisted grant is never ignored;
	// the ordinary (admitted) path never pays for it.
	var live *sharing.GrantIndex
	liveTried := false
	admitted := func(ref sharing.Ref) bool {
		if index.AllowsAll(referrers, ref) {
			return true
		}
		if !liveTried {
			liveTried = true
			if list, listErr := r.Resources.ReferenceGrants().ListLive(ctx); listErr == nil {
				live = sharing.NewGrantIndex(grantsFrom(list))
			} else {
				slog.WarnContext(ctx, "could not read reference grants live; using the informer snapshot",
					"error", listErr)
			}
		}
		return live != nil && live.AllowsAll(referrers, ref)
	}

	targets := make([]referenceTarget, 0, len(spec.MCPs)+len(spec.Skills)+len(spec.KnowledgeBases)+
		len(spec.PromptRefs)+len(spec.CustomCommands)+len(spec.Models)+2)
	add := func(kind, label string, entries []string) {
		for _, entry := range entries {
			if strings.TrimSpace(entry) == "" {
				continue
			}
			targets = append(targets, referenceTarget{kind: kind, label: label, entry: entry})
		}
	}
	add(sharing.KindPrompt, "prompt", spec.PromptRefs)
	add(sharing.KindMCPServer, "mcp", spec.MCPs)
	add(sharing.KindSkill, "skill", spec.Skills)
	add(sharing.KindKnowledgeBase, "knowledge base", spec.KnowledgeBases)
	add(sharing.KindCustomCommand, "custom command", spec.CustomCommands)
	if spec.MemoryEnabled && strings.TrimSpace(spec.MemoryName) != "" {
		targets = append(targets, referenceTarget{sharing.KindMemory, "memory", spec.MemoryName})
	}
	add(sharing.KindModel, "model", spec.Models)

	for _, target := range targets {
		ref := sharing.ResolveKind(referrers[0], target.kind, target.entry)
		if !sharing.Shareable(target.kind) {
			if !sameNamespaceForAll(ref.Namespace, referrers) {
				return referenceRefusal(composed, target, ref, referrers,
					"this kind is not shareable; move it into the referrer's namespace")
			}
			continue
		}
		if _, ok := admin[adminTarget{target.kind, ref.Namespace, ref.Name}]; ok {
			continue
		}
		if admitted(ref) {
			continue
		}
		return referenceRefusal(composed, target, ref, referrers,
			"add a ReferenceGrant in the target namespace, or use a resource in the referrer's namespace")
	}
	return nil
}

// referenceRefusal renders the same decision as a 400 for a composed request
// (the caller can fix the selection) and as a permission error for an ordinary
// run.
func referenceRefusal(composed bool, target referenceTarget, ref sharing.Ref, referrers []string, hint string) error {
	message := fmt.Sprintf("%s %q in namespace %q is not admitted for referrer namespace(s) %s: %s",
		target.label, ref.Name, ref.Namespace, strings.Join(referrers, ", "), hint)
	if composed {
		return &CompositionError{Rejected: []string{message}}
	}
	return errno.ErrPermissionDenied.WithMessage("%s", message)
}

// normalizeReferrers trims, normalizes and deduplicates the namespaces a run
// acts for.
func normalizeReferrers(referrers []string) []string {
	out := make([]string, 0, len(referrers))
	seen := make(map[string]struct{}, len(referrers))
	for _, referrer := range referrers {
		referrer = model.NormalizeNamespace(strings.TrimSpace(referrer))
		if _, dup := seen[referrer]; dup {
			continue
		}
		seen[referrer] = struct{}{}
		out = append(out, referrer)
	}
	sort.Strings(out)
	return out
}

// sameNamespaceForAll reports whether the target lives in every referrer's
// namespace. A single-referrer run reduces to an equality check.
func sameNamespaceForAll(target string, referrers []string) bool {
	for _, referrer := range referrers {
		if target != referrer {
			return false
		}
	}
	return true
}

// referrerNamespaces renders the namespaces of the loaded agent records.
func referrerNamespaces(records []*model.AgentM) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		if record != nil {
			out = append(out, model.NormalizeNamespace(record.Namespace))
		}
	}
	return normalizeReferrers(out)
}

// grantIndex snapshots the grants for one run. The store's List is served by
// the control-plane informer, so this is one in-memory pass.
func (r Resolver) grantIndex(ctx context.Context) (*sharing.GrantIndex, error) {
	_, list, err := r.Resources.ReferenceGrants().List(ctx, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("list reference grants: %w", err)
	}
	return sharing.NewGrantIndex(grantsFrom(list)), nil
}

// grantsFrom flattens the declarative grants into the evaluator's shape.
func grantsFrom(list []*agentv1alpha1.ReferenceGrant) []sharing.Grant {
	grants := make([]sharing.Grant, 0, len(list))
	for _, item := range list {
		if item == nil {
			continue
		}
		for _, from := range item.Spec.From {
			for _, to := range item.Spec.To {
				grants = append(grants, sharing.Grant{
					Namespace:     item.Namespace,
					FromGroup:     from.Group,
					FromKind:      from.Kind,
					FromNamespace: from.Namespace,
					ToGroup:       to.Group,
					ToKind:        to.Kind,
					ToName:        to.Name,
				})
			}
		}
	}
	return grants
}

// adminTarget is one capability a ClusterAgentBinding gives the run directly.
type adminTarget struct {
	kind      string
	namespace string
	name      string
}

// adminTargets collects the capabilities of every ClusterAgentBinding that
// matches one of the run's agents. A cluster-scoped binding is created by an
// administrator, so it is the authorization; the namespace gate must not ask
// for a ReferenceGrant the administrator already stands in for.
func (r Resolver) adminTargets(ctx context.Context, records []*model.AgentM) (map[adminTarget]struct{}, error) {
	if len(records) == 0 {
		return nil, nil
	}
	_, bindings, err := r.Resources.ClusterAgentBindings().List(ctx, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("list cluster agent bindings: %w", err)
	}
	out := make(map[adminTarget]struct{})
	for _, binding := range bindings {
		if binding == nil || !clusterBindingMatches(binding, records) {
			continue
		}
		for _, ref := range binding.Spec.MCPServers {
			out[adminTarget{sharing.KindMCPServer, ref.Namespace, ref.Name}] = struct{}{}
		}
		for _, ref := range binding.Spec.Skills {
			out[adminTarget{sharing.KindSkill, ref.Namespace, ref.Name}] = struct{}{}
		}
		for _, ref := range binding.Spec.KnowledgeBases {
			out[adminTarget{sharing.KindKnowledgeBase, ref.Namespace, ref.Name}] = struct{}{}
		}
		for _, ref := range binding.Spec.Prompts {
			out[adminTarget{sharing.KindPrompt, ref.Namespace, ref.Name}] = struct{}{}
		}
		if binding.Spec.MemoryRef != nil {
			out[adminTarget{sharing.KindMemory, binding.Spec.MemoryRef.Namespace, binding.Spec.MemoryRef.Name}] = struct{}{}
		}
		if binding.Spec.ModelRef != nil {
			out[adminTarget{sharing.KindModel, binding.Spec.ModelRef.Namespace, binding.Spec.ModelRef.Name}] = struct{}{}
		}
	}
	return out, nil
}

// clusterBindingMatches mirrors the controller's audience rule against the
// runtime rows: the namespace list first, then named agents or the selector.
func clusterBindingMatches(binding *agentv1alpha1.ClusterAgentBinding, records []*model.AgentM) bool {
	if binding.Spec.Enabled != nil && !*binding.Spec.Enabled {
		return false
	}
	for _, agent := range records {
		if agent == nil {
			continue
		}
		if !agentvalidation.NamespaceInList(binding.Spec.Namespaces, agent.Namespace) {
			continue
		}
		for _, ref := range binding.Spec.Agents {
			if ref.Namespace == agent.Namespace && ref.Name == agent.Name {
				return true
			}
		}
		if binding.Spec.AgentSelector == nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(binding.Spec.AgentSelector)
		if err == nil && selector.Matches(labels.Set(agent.Labels)) {
			return true
		}
	}
	return false
}
