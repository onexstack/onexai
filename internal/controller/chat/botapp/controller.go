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

package botapp

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/onexstack/onexai/internal/controller/support"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// ControllerName is the canonical name of this controller.
const ControllerName = "botapp-controller"

// Reconciler reconciles a BotApp object.
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
		For(&chatv1alpha1.BotApp{}).
		// A credential rotated into the Secret must re-resolve the application.
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.allInNamespace)).
		Named(ControllerName).
		WithOptions(options).
		Complete(r)
}

// Reconcile reports what a control loop can decide about this object.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &chatv1alpha1.BotApp{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	before := obj.DeepCopy()
	obj.Status.ObservedGeneration = obj.Generation

	ready, reason, message := r.checkSecret(ctx, obj)
	support.SetReady(r.Recorder, obj, &obj.Status.Conditions, obj.Generation, ready, reason, message)

	if apiequality.Semantic.DeepEqual(&before.Status, &obj.Status) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Patch(ctx, obj, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) checkSecret(ctx context.Context, obj *chatv1alpha1.BotApp) (bool, string, string) {
	ref := obj.Spec.CredentialRef
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: obj.Namespace, Name: ref.Name}, secret)
	if err != nil && client.IgnoreNotFound(err) == nil {
		return false, "SecretNotFound", "the credential Secret does not exist in this namespace"
	}
	if err != nil {
		return false, "SecretLookupFailed", err.Error()
	}
	if _, ok := secret.Data[ref.Key]; !ok {
		return false, "SecretKeyNotFound", "the credential Secret has no such key"
	}
	return true, "Accepted", "the application credential is present"
}

func (r *Reconciler) allInNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &chatv1alpha1.BotAppList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to list bot applications for a Secret change", "secret", obj.GetName())
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
