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

package clusteragentbinding

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
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

	"github.com/onexstack/onexai/internal/controller/support"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// ControllerName is the canonical name of this controller.
const ControllerName = "clusteragentbinding-controller"

// Reconciler reconciles a ClusterAgentBinding object.
//
// It resolves the binding once — who it selects and what it contributes — so
// every agent can merge a cheap status read instead of re-resolving the same
// references. The object's own status is also what an operator reads to answer
// "did the platform preset actually reach anyone".
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder publishes Ready transitions as events.
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
		For(&agentv1alpha1.ClusterAgentBinding{}).
		// Every object a binding can name changes what it contributes. The
		// objects are namespace-scoped, so a change is mapped to every cluster
		// binding: the population is a handful of platform presets, and a
		// field index would buy little against the extra machinery.
		Watches(&agentv1alpha1.Agent{}, handler.EnqueueRequestsFromMapFunc(r.allBindings),
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				predicate.LabelChangedPredicate{},
			))).
		Watches(&agentv1alpha1.Prompt{}, handler.EnqueueRequestsFromMapFunc(r.allBindings)).
		Watches(&agentv1alpha1.Skill{}, handler.EnqueueRequestsFromMapFunc(r.allBindings)).
		Watches(&agentv1alpha1.MCPServer{}, handler.EnqueueRequestsFromMapFunc(r.allBindings)).
		Watches(&agentv1alpha1.KnowledgeBase{}, handler.EnqueueRequestsFromMapFunc(r.allBindings)).
		Watches(&agentv1alpha1.CustomCommand{}, handler.EnqueueRequestsFromMapFunc(r.allBindings)).
		// A catalogue entry carries the group label a command selector matches.
		// The selector follows a command added to the group later, so the status
		// has to be recomputed when the catalogue changes.
		Watches(&chatv1alpha1.Command{}, handler.EnqueueRequestsFromMapFunc(r.allBindings),
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				predicate.LabelChangedPredicate{},
			))).
		Watches(&agentv1alpha1.Memory{}, handler.EnqueueRequestsFromMapFunc(r.allBindings)).
		// A wildcard binding's namespace count changes with the namespace set,
		// and an explicit one reports which of its namespaces exist.
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.allBindings)).
		Named(ControllerName).
		WithOptions(options).
		Complete(r)
}

// Reconcile resolves the binding's audience and contributions.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	binding := &agentv1alpha1.ClusterAgentBinding{}
	if err := r.Get(ctx, types.NamespacedName{Name: req.Name}, binding); err != nil {
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
	present, err := r.countNamespaces(ctx, binding.Spec.Namespaces)
	if err != nil {
		return ctrl.Result{}, err
	}

	before := binding.DeepCopy()
	binding.Status.ObservedGeneration = binding.Generation
	binding.Status.MatchedNamespaces = present
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

	namespacesFound := present > 0
	namespaceMessage := fmt.Sprintf("%d of %d declared namespace(s) exist",
		present, len(binding.Spec.Namespaces))
	if agentvalidation.IsAllNamespaces(binding.Spec.Namespaces) {
		namespaceMessage = fmt.Sprintf("all %d namespace(s) match (wildcard %q)",
			present, agentv1alpha1.AllNamespaces)
	}
	apimeta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionNamespaceMatched,
		Status:             boolCondition(namespacesFound),
		Reason:             reasonFor(namespacesFound, "NamespacesFound", "NoMatchingNamespaces"),
		Message:            namespaceMessage,
		ObservedGeneration: binding.Generation,
	})

	support.SetReady(r.Recorder, binding, &binding.Status.Conditions, binding.Generation, refsResolved,
		reasonFor(refsResolved, "Resolved", "ResolutionFailed"),
		fmt.Sprintf("%d agent(s) across %d namespace(s) match; %d reference(s) missing",
			matched, present, len(contribution.Missing)))

	if apiequality.Semantic.DeepEqual(&before.Status, &binding.Status) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Patch(ctx, binding, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

// countNamespaces reports how many of the declared namespaces exist. For the
// wildcard, that is every namespace the control plane holds.
func (r *Reconciler) countNamespaces(ctx context.Context, namespaces []string) (int32, error) {
	if agentvalidation.IsAllNamespaces(namespaces) {
		list := &corev1.NamespaceList{}
		if err := r.List(ctx, list); err != nil {
			return 0, err
		}
		return int32(len(list.Items)), nil
	}

	var present int32
	for _, namespace := range namespaces {
		obj := &corev1.Namespace{}
		if err := r.Get(ctx, types.NamespacedName{Name: namespace}, obj); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue
			}
			return 0, err
		}
		present++
	}
	return present, nil
}

// allBindings enqueues every cluster binding.
func (r *Reconciler) allBindings(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &agentv1alpha1.ClusterAgentBindingList{}
	if err := r.List(ctx, list); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to list cluster agent bindings")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: list.Items[i].Name},
		})
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

func missingMessage(missing []string) string {
	if len(missing) == 0 {
		return "every referenced object exists and is enabled"
	}
	return "missing or disabled: " + strings.Join(missing, ", ")
}
