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

package mcpserver

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/onexstack/onexai/internal/controller/support"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// ControllerName is the canonical name of this controller.
const ControllerName = "mcpserver-controller"

// Reconciler reconciles a MCPServer object.
//
// The MCP handshake and tool discovery are reported by the runtime, which owns the
// client and the connection cache. What is checked here is the header Secret.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder publishes Ready transitions as events, so `kubectl describe`
	// shows what the controller decided without opening the status.
	Recorder record.EventRecorder
	// APIReader bypasses the cache. It is used to confirm that a server is
	// really gone before deleting its orphaned grant, so a recreated object is
	// not deprived of its rule because the cache was one event behind.
	APIReader client.Reader
}

// Add wires the reconciler into the manager.
func Add(ctx context.Context, mgr ctrl.Manager, options controller.Options) (bool, error) {
	r := &Reconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorderFor(ControllerName),
		APIReader: mgr.GetAPIReader(),
	}
	if err := r.SetupWithManager(ctx, mgr, options); err != nil {
		return false, err
	}
	return true, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.MCPServer{}).
		// A credential rotated into the Secret must re-resolve this object.
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.allInNamespace)).
		// The auto-grant rule is derived state. Watching it lets the controller
		// rebuild one that was deleted and finish the cleanup after a server is
		// removed; the predicate keeps unrelated rules out of this loop.
		Watches(&agentv1alpha1.AuthorizationRule{},
			handler.EnqueueRequestsFromMapFunc(r.mcpserverForGrantRule),
			// Only rules this controller owns can feed back into an MCPServer
			// reconcile, and only a spec change (or a create/delete) can change
			// the answer: the status updates the authorizationrule controller
			// writes are not an input here.
			builder.WithPredicates(predicate.And[client.Object](
				predicate.NewPredicateFuncs(isManagedGrantRuleObject),
				predicate.GenerationChangedPredicate{},
			)),
		).
		Named(ControllerName).
		WithOptions(options).
		Complete(r)
}

// Reconcile reports what a control loop can decide without calling the external
// system.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &agentv1alpha1.MCPServer{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		if apierrors.IsNotFound(err) {
			// The server is gone. Remove the rule this controller created for it;
			// the ownerReference is the primary cleanup path, and this covers a
			// deployment where the garbage collector is off.
			return ctrl.Result{}, r.cleanupAutoGrant(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}

	before := obj.DeepCopy()
	obj.Status.ObservedGeneration = obj.Generation

	ready, reason, message := r.checkSecret(ctx, obj)
	support.SetReady(r.Recorder, obj, &obj.Status.Conditions, obj.Generation, ready, reason, message)

	if !apiequality.Semantic.DeepEqual(&before.Status, &obj.Status) {
		if err := r.Status().Patch(ctx, obj, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.ensureAutoGrant(ctx, obj); err != nil {
		if errors.Is(err, errGrantTerminating) {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) checkSecret(ctx context.Context, obj *agentv1alpha1.MCPServer) (bool, string, string) {
	ref := obj.Spec.HeadersRef
	if ref == nil {
		return true, "Accepted", "no header Secret referenced"
	}
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: obj.Namespace, Name: ref.Name}, secret)
	if err != nil && client.IgnoreNotFound(err) == nil {
		return false, "SecretNotFound", "the header Secret does not exist in this namespace"
	}
	if err != nil {
		return false, "SecretLookupFailed", err.Error()
	}
	return true, "Accepted", "the referenced Secret is present"
}

func (r *Reconciler) allInNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &agentv1alpha1.MCPServerList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to list mcpservers for a Secret change", "secret", obj.GetName())
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: list.Items[i].Namespace, Name: list.Items[i].Name,
		}})
	}
	return requests
}
