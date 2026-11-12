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

package dutygroup

import (
	"context"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/onexstack/onexai/internal/controller/support"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// ControllerName is the canonical name of this controller.
const ControllerName = "dutygroup-controller"

// Reconciler reconciles a DutyGroup object.
//
// The roster itself lives in Regen; reconciling it against that system is a
// separate step that needs Regen credentials, so this controller reports what
// the spec declares and leaves RosterReady to that step.
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
		For(&chatv1alpha1.DutyGroup{}).
		Named(ControllerName).
		WithOptions(options).
		Complete(r)
}

// Reconcile reports whether a schedule is configured for the group.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	group := &chatv1alpha1.DutyGroup{}
	if err := r.Get(ctx, req.NamespacedName, group); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	before := group.DeepCopy()
	group.Status.ObservedGeneration = group.Generation

	configured := group.Spec.ScheduleID != "" || group.Spec.ScheduleRef != nil
	apimeta.SetStatusCondition(&group.Status.Conditions, metav1.Condition{
		Type:               chatv1alpha1.ConditionScheduleResolved,
		Status:             boolCondition(configured),
		Reason:             reasonFor(configured, "ScheduleConfigured", "NoScheduleConfigured"),
		Message:            scheduleMessage(configured),
		ObservedGeneration: group.Generation,
	})
	support.SetReady(r.Recorder, group, &group.Status.Conditions, group.Generation, true, "Accepted",
		"the group is well formed; the roster is read from Regen by the duty scheduler")

	if apiequality.Semantic.DeepEqual(&before.Status, &group.Status) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Patch(ctx, group, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func scheduleMessage(configured bool) string {
	if configured {
		return "a Regen schedule is configured for this group"
	}
	return "no schedule is configured; the roster falls back to the group name"
}

func reasonFor(ok bool, whenTrue, whenFalse string) string {
	if ok {
		return whenTrue
	}
	return whenFalse
}

func boolCondition(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}
