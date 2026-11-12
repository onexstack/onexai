// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package authorizationrule

import (
	"context"
	"fmt"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// ControllerName is the canonical name of this controller, used by the
// controller-manager's --controllers flag to enable/disable it.
const ControllerName = "authorizationrule-controller"

// Condition types reported on an AuthorizationRule.
const (
	// ConditionLoaded reports that the controller has seen and understood the
	// rule. It is always true for a rule the API server accepted; a false value
	// means the controller could not resolve the rule's scope.
	ConditionLoaded = "Loaded"
	// ConditionMatchesAllAgents warns that the rule is not scoped to particular
	// agents and therefore answers for every agent in the namespace.
	ConditionMatchesAllAgents = "MatchesAllAgents"
)

// Reconciler reconciles an AuthorizationRule object.
//
// The controller does not load the rule into anything: the agent runtime reads
// rules through a watch and assembles the Rego data itself. What this controller
// adds is what a runtime cannot: a resolved view of the rule's scope, so an
// operator can see that a selector matches three agents today and that an
// unscoped rule answers for all of them.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder publishes Ready transitions as events, so `kubectl describe`
	// shows what the controller decided without opening the status.
	Recorder record.EventRecorder
}

// Add wires the reconciler into the given manager. It is invoked by the
// controller-manager's generated descriptor (cmd/<binary>/app/controllers.go).
func Add(ctx context.Context, mgr ctrl.Manager, options controller.Options) (bool, error) {
	r := &Reconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor(ControllerName),
	}
	if err := r.SetupWithManager(ctx, mgr, options); err != nil {
		return false, err
	}
	return true, nil
}

// SetupWithManager sets up the controller with the Manager.
//
// It also watches Agents: adding, relabelling or removing one changes which
// rules apply to it, and the rule's status would otherwise be stale until
// somebody edited the rule itself.
//
// # Why both watches are filtered
//
// Each rule reconcile lists every agent in its namespace (matchedAgents), and
// each agent event enqueues every rule in the namespace (rulesForAgent). Left
// unfiltered, a status write on one agent fans out into a list-per-rule storm:
// with a few hundred rules and steady agent status churn this controller holds
// multiple cores permanently busy. Only the inputs to matching are watched —
// the rule's spec (its generation) and the agent's labels — the same pair the
// agent controller already uses. A status write is nobody's input and is
// ignored.
func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	matchingChanged := predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.LabelChangedPredicate{},
	)
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.AuthorizationRule{}, builder.WithPredicates(matchingChanged)).
		Watches(&agentv1alpha1.Agent{},
			handler.EnqueueRequestsFromMapFunc(r.rulesForAgent),
			builder.WithPredicates(matchingChanged),
		).
		Named(ControllerName).
		WithOptions(options).
		Complete(r)
}

// Reconcile resolves the rule's agent scope and reports it in status.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	rule := &agentv1alpha1.AuthorizationRule{}
	if err := r.Get(ctx, req.NamespacedName, rule); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	matched, err := r.matchedAgents(ctx, rule)
	if err != nil {
		return ctrl.Result{}, err
	}

	status := rule.Status.DeepCopy()
	status.ObservedGeneration = rule.Generation
	status.MatchedAgents = int32(len(matched))

	apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               ConditionLoaded,
		Status:             metav1.ConditionTrue,
		Reason:             "RuleAccepted",
		Message:            "the rule was accepted by the API server and its scope resolved",
		ObservedGeneration: rule.Generation,
	})

	unscoped := isUnscoped(rule.Spec)
	condition := metav1.Condition{
		Type:               ConditionMatchesAllAgents,
		Status:             metav1.ConditionFalse,
		Reason:             "Scoped",
		Message:            fmt.Sprintf("the rule applies to %d agent(s) in this namespace", len(matched)),
		ObservedGeneration: rule.Generation,
	}
	if unscoped {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "Unscoped"
		condition.Message = "the rule names no agents and carries no selector, so it answers for every agent in this namespace"
	}
	apimeta.SetStatusCondition(&status.Conditions, condition)

	if apiequality.Semantic.DeepEqual(&rule.Status, status) {
		return ctrl.Result{}, nil
	}

	// A single writer owns status, so a merge patch is enough; server-side apply
	// is reserved for the resources where the runtime also writes conditions.
	before := rule.DeepCopy()
	rule.Status = *status
	if err := r.Status().Patch(ctx, rule, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, err
	}
	log.V(1).Info("resolved authorization rule scope", "rule", rule.Name, "matchedAgents", len(matched), "unscoped", unscoped)
	return ctrl.Result{}, nil
}

// matchedAgents returns the agents in the rule's namespace that its name list
// and selector address.
func (r *Reconciler) matchedAgents(ctx context.Context, rule *agentv1alpha1.AuthorizationRule) ([]agentv1alpha1.Agent, error) {
	agents := &agentv1alpha1.AgentList{}
	if err := r.List(ctx, agents, client.InNamespace(rule.Namespace)); err != nil {
		return nil, err
	}

	named := make(map[string]struct{}, len(rule.Spec.AgentNames))
	for _, name := range rule.Spec.AgentNames {
		named[name] = struct{}{}
	}
	var selector labels.Selector
	if rule.Spec.AgentSelector != nil {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(rule.Spec.AgentSelector)
		if err != nil {
			// The API server validated this selector on write; a failure here
			// means the stored object is invalid, which is worth reporting.
			return nil, err
		}
	}

	var matched []agentv1alpha1.Agent
	for i := range agents.Items {
		agent := &agents.Items[i]
		if _, ok := named[agent.Name]; ok {
			matched = append(matched, *agent)
			continue
		}
		if selector != nil && selector.Matches(labels.Set(agent.Labels)) {
			matched = append(matched, *agent)
		}
	}
	return matched, nil
}

// isUnscoped reports whether the rule answers for every agent: no names and no
// selector, or an explicitly empty selector (which selects everything).
func isUnscoped(spec agentv1alpha1.AuthorizationRuleSpec) bool {
	if len(spec.AgentNames) > 0 {
		return false
	}
	if spec.AgentSelector == nil {
		return true
	}
	selector, err := metav1.LabelSelectorAsSelector(spec.AgentSelector)
	return err == nil && selector.Empty()
}

// rulesForAgent enqueues every AuthorizationRule in the agent's namespace when
// that agent changes. Namespaces are small and rules are few, so a list is
// cheaper than an index that would have to keep selectors resolved.
func (r *Reconciler) rulesForAgent(ctx context.Context, obj client.Object) []reconcile.Request {
	rules := &agentv1alpha1.AuthorizationRuleList{}
	if err := r.List(ctx, rules, client.InNamespace(obj.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to list authorization rules for agent", "agent", obj.GetName())
		return nil
	}

	requests := make([]reconcile.Request, 0, len(rules.Items))
	for i := range rules.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: rules.Items[i].Namespace,
				Name:      rules.Items[i].Name,
			},
		})
	}
	return requests
}
