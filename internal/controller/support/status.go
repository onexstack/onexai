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

// Package support holds the small pieces the controllers of both API groups
// share.
package support

import (
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
)

// ReadyCondition is the aggregate condition every resource reports.
const ReadyCondition = "Ready"

// SetReady records the aggregate readiness of a resource.
//
// Live health (a model endpoint answering, an MCP server completing its
// handshake) is reported by the runtime that actually talks to those systems,
// under its own field manager. This condition only covers what a control loop
// can decide without making the call.
func SetReady(
	recorder record.EventRecorder,
	obj runtime.Object,
	conditions *[]metav1.Condition,
	generation int64,
	ready bool,
	reason, message string,
) {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}

	ReportCondition(recorder, obj, conditions, metav1.Condition{
		Type:               ReadyCondition,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

// ReportCondition records a condition and, when the Ready condition changes,
// emits an event on the object.
//
// Conditions are for a client that polls; events are for a person reading
// `kubectl describe`. Without the event, a controller that has never become
// ready looks exactly like one that is working: its verdict lives in a status
// field nobody opened. Only the aggregate condition produces events — the
// derived ones (RefsResolved, SkillsWithinBudget, ...) would triple the event
// stream to say the same thing.
//
// Only transitions are reported. A controller that re-reports the same verdict
// every reconcile would otherwise bury every other event in the object's
// history.
func ReportCondition(
	recorder record.EventRecorder,
	obj runtime.Object,
	conditions *[]metav1.Condition,
	condition metav1.Condition,
) {
	before := apimeta.FindStatusCondition(*conditions, condition.Type)

	apimeta.SetStatusCondition(conditions, condition)

	if recorder == nil || condition.Type != ReadyCondition {
		return
	}

	if before != nil && before.Status == condition.Status {
		return
	}

	eventType := corev1.EventTypeNormal
	if condition.Status != metav1.ConditionTrue {
		eventType = corev1.EventTypeWarning
	}

	recorder.Event(obj, eventType, condition.Reason, condition.Message)
}
