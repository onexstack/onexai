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

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// errGrantTerminating is returned while the auto-grant rule is being deleted.
// The caller requeues instead of writing to an object the API server is about
// to remove.
var errGrantTerminating = errors.New("the auto-grant authorization rule is being deleted")

// maxRuleNameLength is the DNS subdomain limit the control plane enforces on
// object names.
const maxRuleNameLength = 253

// ensureAutoGrant makes the server-level allow rule agree with the MCPServer.
//
// # Why this exists
//
// The resource-reference check is fail-closed (see
// internal/agent/biz/v1/agent/gate.go): an MCP server with no AuthorizationRule
// is refused at run time with "mcp:<name>: authorization: all levels NoOpinion
// (fail-closed)". Owning the Agent does not exempt its references either. A
// server therefore needs a rule the moment it exists, or every Agent that names
// it is refused.
//
// The rule is server-scoped and names every user, the same shape as OneX's
// model.MCPGrantRuleNameFor rows and the seed's onexai-grant: it grants the
// *server*, while "who may run an Agent that references it" stays a per-Agent
// decision in that Agent's own rules.
//
// When a rule with the conventional name already exists and was not created
// here, it is never rewritten. That keeps OneX and operator-authored rules
// authoritative; a conflict is reported as an event rather than silently
// replaced.
func (r *Reconciler) ensureAutoGrant(ctx context.Context, mcp *agentv1alpha1.MCPServer) error {
	ns := normalizeServerNamespace(mcp.Namespace)
	name := agentv1alpha1.MCPGrantRuleName(mcp.Name)

	if autoGrantOptedOut(mcp) {
		// Opting out means "do not manage"; remove only what this controller
		// created, never an operator's or OneX's rule.
		return r.deleteManagedGrant(ctx, ns, mcp.Name)
	}

	if len(name) > maxRuleNameLength {
		r.event(mcp, corev1.EventTypeWarning, "GrantRuleNameTooLong",
			fmt.Sprintf("cannot create authorization rule %q: the name exceeds %d characters", name, maxRuleNameLength))
		// Retrying cannot change the name; an operator must rename the server or
		// author the rule by hand.
		return nil
	}

	existing := &agentv1alpha1.AuthorizationRule{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, existing)
	switch {
	case err == nil:
	case apierrors.IsNotFound(err):
		existing = nil
	default:
		return err
	}

	if existing != nil {
		if !existing.DeletionTimestamp.IsZero() {
			return errGrantTerminating
		}
		if !isManagedGrantRule(existing, ns, mcp.Name) {
			if grantRuleCovers(existing, mcp) {
				// An operator's or OneX's rule already grants this server.
				return nil
			}
			r.event(mcp, corev1.EventTypeWarning, "GrantRuleConflict",
				fmt.Sprintf("authorization rule %s/%s already exists but does not grant this server; leaving it untouched", ns, name))
			return nil
		}
		return r.updateManagedGrant(ctx, mcp, existing)
	}

	if !mcp.Spec.Enabled {
		return nil
	}

	if err := r.Create(ctx, newGrantRule(mcp)); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Raced with OneX or an operator. Whatever is there wins.
			return nil
		}
		return err
	}
	r.event(mcp, corev1.EventTypeNormal, "GrantRuleCreated",
		fmt.Sprintf("created authorization rule %s/%s", ns, name))
	return nil
}

// cleanupAutoGrant removes the rule this controller created for a server that
// is gone. It is the second line of defence: the rule's ownerReference lets the
// garbage collector remove it even when this process is down.
//
// The live read deliberately bypasses the cache: a delete event can race with a
// recreate of the same name, and a lagging cache must not deprive the new
// server of its rule.
func (r *Reconciler) cleanupAutoGrant(ctx context.Context, key types.NamespacedName) error {
	live := &agentv1alpha1.MCPServer{}
	err := r.reader().Get(ctx, key, live)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return r.deleteManagedGrant(ctx, normalizeServerNamespace(key.Namespace), key.Name)
}

// deleteManagedGrant deletes the auto-grant rule for a server if (and only if)
// this controller created it.
func (r *Reconciler) deleteManagedGrant(ctx context.Context, ns, serverName string) error {
	rule := &agentv1alpha1.AuthorizationRule{}
	err := r.reader().Get(ctx, types.NamespacedName{
		Namespace: ns,
		Name:      agentv1alpha1.MCPGrantRuleName(serverName),
	}, rule)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isManagedGrantRule(rule, ns, serverName) {
		return nil
	}
	if err := r.Delete(ctx, rule); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// updateManagedGrant brings a rule this controller owns back to the desired
// shape. It never touches a rule it does not own.
func (r *Reconciler) updateManagedGrant(
	ctx context.Context, mcp *agentv1alpha1.MCPServer, existing *agentv1alpha1.AuthorizationRule,
) error {
	desired := newGrantRule(mcp)
	before := existing.DeepCopy()

	existing.Labels = mergeStringMap(existing.Labels, desired.Labels)
	existing.Annotations = mergeStringMap(existing.Annotations, desired.Annotations)
	existing.OwnerReferences = desired.OwnerReferences
	existing.Spec = desired.Spec

	if apiequality.Semantic.DeepEqual(before, existing) {
		return nil
	}
	if err := r.Patch(ctx, existing, client.MergeFrom(before)); err != nil {
		return err
	}
	r.event(mcp, corev1.EventTypeNormal, "GrantRuleUpdated",
		fmt.Sprintf("updated authorization rule %s/%s", existing.Namespace, existing.Name))
	return nil
}

// newGrantRule builds the server-level allow rule for one MCPServer.
func newGrantRule(mcp *agentv1alpha1.MCPServer) *agentv1alpha1.AuthorizationRule {
	ns := normalizeServerNamespace(mcp.Namespace)
	names := []string{mcp.Name}
	if canonical := ns + "/" + mcp.Name; canonical != mcp.Name {
		// The bare name matches the canonical reference in any namespace (the
		// historical contract); the qualified one names this object exactly.
		names = append(names, canonical)
	}

	meta := metav1.ObjectMeta{
		Name:      agentv1alpha1.MCPGrantRuleName(mcp.Name),
		Namespace: ns,
		Labels: map[string]string{
			agentv1alpha1.LabelGrantManagedBy: agentv1alpha1.ManagedByMCPServerController,
		},
		Annotations: map[string]string{
			agentv1alpha1.AnnotationGrantRef:      ns + "/" + mcp.Name,
			agentv1alpha1.AnnotationGrantOwnerUID: string(mcp.UID),
		},
	}
	// An ownerReference requires a UID. A unit-test object may not have one; the
	// NotFound cleanup still works without it.
	if mcp.UID != "" {
		meta.OwnerReferences = []metav1.OwnerReference{
			agentv1alpha1.MCPServerOwnerReference(mcp.Name, mcp.UID),
		}
	}

	return &agentv1alpha1.AuthorizationRule{
		ObjectMeta: meta,
		Spec: agentv1alpha1.AuthorizationRuleSpec{
			Enabled: mcp.Spec.Enabled,
			Description: fmt.Sprintf(
				"Auto-generated for MCPServer %s/%s. Allows the MCP server to be referenced by agents; "+
					"an explicit effect=Deny rule wins, and who may run an agent that references it is "+
					"gated by that agent's own rules. Set %s=false on the MCPServer to opt out.",
				ns, mcp.Name, agentv1alpha1.MCPServerAnnotationAutoGrant),
			Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "*"}},
			Resource: agentv1alpha1.AuthorizationResource{Kind: "mcp", Names: names},
			Verbs:    []string{"get", "list", "watch"},
			Effect:   agentv1alpha1.EffectAllow,
		},
	}
}

// isManagedGrantRule reports whether this controller created the rule for the
// named server. The label alone is not enough: the annotation ties the rule to
// one server, so a copied or hand-edited object cannot be mistaken for ours.
func isManagedGrantRule(rule *agentv1alpha1.AuthorizationRule, ns, serverName string) bool {
	if rule == nil {
		return false
	}
	if rule.Labels[agentv1alpha1.LabelGrantManagedBy] != agentv1alpha1.ManagedByMCPServerController {
		return false
	}
	return rule.Annotations[agentv1alpha1.AnnotationGrantRef] == ns+"/"+serverName
}

// isManagedGrantRuleObject is the watch predicate: only rules this controller
// owns can feed back into an MCPServer reconcile.
func isManagedGrantRuleObject(obj client.Object) bool {
	return obj != nil && obj.GetLabels()[agentv1alpha1.LabelGrantManagedBy] == agentv1alpha1.ManagedByMCPServerController
}

// grantRuleCovers reports whether an existing rule already grants the server to
// every user without an agent scope. A narrower rule is not a substitute for
// the baseline; it is reported as a conflict instead.
func grantRuleCovers(rule *agentv1alpha1.AuthorizationRule, mcp *agentv1alpha1.MCPServer) bool {
	if rule == nil || !rule.Spec.Enabled || rule.Spec.Effect != agentv1alpha1.EffectAllow {
		return false
	}
	if rule.Spec.Resource.Kind != "mcp" {
		return false
	}
	// An agent-scoped grant answers for those agents only; it cannot stand in
	// for the server-wide baseline.
	if len(rule.Spec.AgentNames) > 0 || rule.Spec.AgentSelector != nil {
		return false
	}

	ns := normalizeServerNamespace(mcp.Namespace)
	canonical := ns + "/" + mcp.Name
	coversName := false
	for _, name := range rule.Spec.Resource.Names {
		if name == mcp.Name || name == canonical || name == "*" {
			coversName = true
			break
		}
	}
	if !coversName {
		return false
	}

	for _, subject := range rule.Spec.Subjects {
		if subject.Kind == rbacv1.UserKind && subject.Name == "*" {
			return true
		}
	}
	return false
}

// autoGrantOptedOut reports whether the server asks this controller not to
// manage its grant.
func autoGrantOptedOut(mcp *agentv1alpha1.MCPServer) bool {
	value, ok := mcp.Annotations[agentv1alpha1.MCPServerAnnotationAutoGrant]
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(value), "false")
}

// normalizeServerNamespace mirrors the runtime's namespace default.
func normalizeServerNamespace(namespace string) string {
	return sharing.Normalize(namespace)
}

// mergeStringMap returns dst with src's entries added, without mutating either
// input.
func mergeStringMap(dst, src map[string]string) map[string]string {
	if dst == nil {
		dst = make(map[string]string, len(src))
	}
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

// mcpserverForGrantRule maps a rule change back to the MCPServer that owns it.
// The annotation, not the rule name, is authoritative: a rule this controller
// created always records its owner.
func (r *Reconciler) mcpserverForGrantRule(_ context.Context, obj client.Object) []reconcile.Request {
	ref := obj.GetAnnotations()[agentv1alpha1.AnnotationGrantRef]
	ns, name, ok := strings.Cut(ref, "/")
	if !ok || ns == "" || name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}}
}

// reader returns the uncached reader when one is configured. The cache is
// correct for the normal reconcile path; it is not correct for deciding that an
// object is gone.
func (r *Reconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *Reconciler) event(obj runtime.Object, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(obj, eventType, reason, message)
}
