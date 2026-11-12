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

package agentbinding

import (
	"context"
	"fmt"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	"github.com/onexstack/onexai/internal/controller/agent/grant"
	"github.com/onexstack/onexai/internal/controller/support"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// ControllerName is the canonical name of this controller.
const ControllerName = "agentbinding-controller"

// Reconciler reconciles an AgentBinding object.
//
// It resolves the binding once — who it selects and what it contributes — so
// every agent in the namespace can merge a cheap status read instead of
// re-resolving the same references.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder publishes Ready transitions as events, so `kubectl describe`
	// shows what the controller decided without opening the status.
	Recorder record.EventRecorder
}

// Add wires the reconciler into the manager.
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
func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.AgentBinding{}).
		// Every object a binding can name changes what it contributes. Watching
		// agents also covers label edits, which change who it selects.
		Watches(&agentv1alpha1.Agent{}, handler.EnqueueRequestsFromMapFunc(r.bindingsInNamespace),
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				predicate.LabelChangedPredicate{},
			))).
		Watches(&agentv1alpha1.Prompt{}, handler.EnqueueRequestsFromMapFunc(r.bindingsInNamespace)).
		Watches(&agentv1alpha1.Skill{}, handler.EnqueueRequestsFromMapFunc(r.bindingsInNamespace)).
		Watches(&agentv1alpha1.MCPServer{}, handler.EnqueueRequestsFromMapFunc(r.bindingsInNamespace)).
		Watches(&agentv1alpha1.KnowledgeBase{}, handler.EnqueueRequestsFromMapFunc(r.bindingsInNamespace)).
		Watches(&agentv1alpha1.CustomCommand{}, handler.EnqueueRequestsFromMapFunc(r.bindingsInNamespace)).
		// A catalogue entry carries the group label a command selector matches.
		// A new command in that group — or a relabelled one — changes what the
		// binding grants, and its generation does not change.
		Watches(&chatv1alpha1.Command{}, handler.EnqueueRequestsFromMapFunc(r.bindingsInNamespace),
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				predicate.LabelChangedPredicate{},
			))).
		// A grant in the target namespace changes what bindings in its from
		// namespaces may resolve, so the bindings in those namespaces are the
		// ones that need re-evaluating.
		Watches(&agentv1alpha1.ReferenceGrant{},
			handler.EnqueueRequestsFromMapFunc(r.bindingsForGrant),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named(ControllerName).
		WithOptions(options).
		Complete(r)
}

// Reconcile resolves the binding's agent scope and contributions.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	binding := &agentv1alpha1.AgentBinding{}
	if err := r.Get(ctx, req.NamespacedName, binding); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if binding.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	contribution, err := Compute(ctx, r.Client, binding)
	if err != nil {
		return ctrl.Result{}, err
	}
	matched, err := MatchAgents(ctx, r.Client, binding)
	if err != nil {
		return ctrl.Result{}, err
	}

	before := binding.DeepCopy()
	binding.Status.ObservedGeneration = binding.Generation
	binding.Status.MatchedAgents = matched
	binding.Status.Resolved = agentv1alpha1.AgentBindingResolved{
		Prompts:        contribution.Prompts,
		Skills:         contribution.Skills,
		MCPs:           contribution.MCPs,
		KnowledgeBases: contribution.KnowledgeBases,
		CustomCommands: contribution.CustomCommands,
		Commands:       contribution.Commands,
	}
	binding.Status.Missing = contribution.Missing

	refsResolved := len(contribution.Missing) == 0
	apimeta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionRefsResolved,
		Status:             boolCondition(refsResolved),
		Reason:             reasonFor(refsResolved, "AllRefsFound", "ReferenceNotFound"),
		Message:            missingMessage(contribution.Missing),
		ObservedGeneration: binding.Generation,
	})

	agentsMatched := matched > 0
	apimeta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionAgentsMatched,
		Status:             boolCondition(agentsMatched),
		Reason:             reasonFor(agentsMatched, "AgentsMatched", "NoMatchingAgents"),
		Message:            matchedMessage(matched),
		ObservedGeneration: binding.Generation,
	})

	if SelectorMatchesAll(binding) {
		apimeta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
			Type:               agentv1alpha1.ConditionSelectorMatchesAllAgents,
			Status:             metav1.ConditionTrue,
			Reason:             "EmptySelector",
			Message:            "the selector is empty, so the binding applies to every agent in the namespace",
			ObservedGeneration: binding.Generation,
		})
	} else {
		apimeta.RemoveStatusCondition(&binding.Status.Conditions, agentv1alpha1.ConditionSelectorMatchesAllAgents)
	}

	support.SetReady(r.Recorder, binding, &binding.Status.Conditions, binding.Generation, refsResolved,
		reasonFor(refsResolved, "Resolved", "ResolutionFailed"),
		fmt.Sprintf("%d agent(s) match; %d reference(s) missing", matched, len(contribution.Missing)))

	if apiequality.Semantic.DeepEqual(&before.Status, &binding.Status) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Patch(ctx, binding, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

// bindingsInNamespace enqueues every binding in the changed object's namespace.
func (r *Reconciler) bindingsInNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &agentv1alpha1.AgentBindingList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to list agentbindings", "object", obj.GetName())
		return nil
	}

	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: list.Items[i].Namespace, Name: list.Items[i].Name},
		})
	}
	return requests
}

// bindingsForGrant enqueues the bindings a grant can affect: the ones in the
// namespaces its from list names.
func (r *Reconciler) bindingsForGrant(ctx context.Context, obj client.Object) []reconcile.Request {
	refGrant, ok := obj.(*agentv1alpha1.ReferenceGrant)
	if !ok {
		return nil
	}
	seen := make(map[types.NamespacedName]struct{})
	var requests []reconcile.Request
	for _, from := range refGrant.Spec.From {
		if from.Group != sharing.GroupName || from.Kind != grant.AgentKind {
			continue
		}
		var bindings agentv1alpha1.AgentBindingList
		if err := r.List(ctx, &bindings, client.InNamespace(from.Namespace)); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "failed to list agentbindings for a reference grant",
				"grant", refGrant.Name, "namespace", refGrant.Namespace)
			continue
		}
		for i := range bindings.Items {
			key := types.NamespacedName{Namespace: bindings.Items[i].Namespace, Name: bindings.Items[i].Name}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			requests = append(requests, reconcile.Request{NamespacedName: key})
		}
	}
	return requests
}

func boolCondition(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func reasonFor(ok bool, whenTrue, whenFalse string) string {
	if ok {
		return whenTrue
	}
	return whenFalse
}

func matchedMessage(matched int32) string {
	if matched == 0 {
		return "the selector and name list match no enabled agent; the binding contributes nothing"
	}
	return "the binding selects the matched agents"
}

func missingMessage(missing []string) string {
	if len(missing) == 0 {
		return "every referenced object exists and is enabled"
	}
	return "missing or disabled: " + strings.Join(missing, ", ")
}
