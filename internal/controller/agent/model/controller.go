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

package model

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/onexstack/onexai/internal/controller/support"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// ControllerName is the canonical name of this controller.
const ControllerName = "model-controller"

// Reconciler reconciles a Model object.
//
// It checks what can be checked without calling the endpoint: the credential
// Secret and the key in it exist. Reachability and auth results are published by
// the runtime that actually talks to the provider, under its own field manager —
// duplicated HTTP clients with their own SSRF policy would be a second truth.
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
		For(&agentv1alpha1.Model{}).
		// A credential rotated into the Secret must re-resolve the model, and a
		// deleted one must stop it being Ready.
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.modelsInNamespace)).
		Named(ControllerName).
		WithOptions(options).
		Complete(r)
}

// Reconcile checks the model's credential reference.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	model := &agentv1alpha1.Model{}
	if err := r.Get(ctx, req.NamespacedName, model); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	before := model.DeepCopy()
	model.Status.ObservedGeneration = model.Generation

	ready, reason, message := r.checkCredential(ctx, model)
	support.SetReady(r.Recorder, model, &model.Status.Conditions, model.Generation, ready, reason, message)

	if apiequality.Semantic.DeepEqual(&before.Status, &model.Status) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Patch(ctx, model, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) checkCredential(ctx context.Context, model *agentv1alpha1.Model) (bool, string, string) {
	ref := model.Spec.CredentialRef
	if ref == nil {
		return true, "Accepted", fmt.Sprintf("endpoint %s declared without a credential reference", model.Spec.Endpoint)
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: model.Namespace, Name: ref.Name}, secret); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return false, "SecretNotFound", fmt.Sprintf("credential Secret %q does not exist in %s", ref.Name, model.Namespace)
		}
		return false, "SecretLookupFailed", err.Error()
	}
	if _, ok := secret.Data[ref.Key]; !ok {
		return false, "SecretKeyNotFound", fmt.Sprintf("credential Secret %q has no key %q", ref.Name, ref.Key)
	}
	return true, "Accepted", fmt.Sprintf("credential %s/%s is present", ref.Name, ref.Key)
}

func (r *Reconciler) modelsInNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	models := &agentv1alpha1.ModelList{}
	if err := r.List(ctx, models, client.InNamespace(obj.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to list models for a Secret change", "secret", obj.GetName())
		return nil
	}
	requests := make([]reconcile.Request, 0, len(models.Items))
	for i := range models.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: models.Items[i].Namespace, Name: models.Items[i].Name,
		}})
	}
	return requests
}
