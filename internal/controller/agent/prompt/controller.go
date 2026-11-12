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

package prompt

import (
	"context"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/onexstack/onexai/internal/controller/support"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// ControllerName is the canonical name of this controller.
const ControllerName = "prompt-controller"

// Reconciler reconciles a Prompt object.
//
// A prompt has nothing external to check; the controller exists so the object
// reports an observedGeneration and a Ready condition like every other resource.
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
		For(&agentv1alpha1.Prompt{}).
		Named(ControllerName).
		WithOptions(options).
		Complete(r)
}

// Reconcile reports what a control loop can decide without calling the external
// system.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &agentv1alpha1.Prompt{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	before := obj.DeepCopy()
	obj.Status.ObservedGeneration = obj.Generation

	support.SetReady(r.Recorder, obj, &obj.Status.Conditions, obj.Generation, true, "Accepted",
		"the object is well formed; live health is reported by the runtime")

	if apiequality.Semantic.DeepEqual(&before.Status, &obj.Status) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Patch(ctx, obj, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
