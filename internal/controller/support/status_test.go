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

package support

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func target() *agentv1alpha1.Skill {
	obj := &agentv1alpha1.Skill{}
	obj.Name = "lint"
	obj.Namespace = "default"
	return obj
}

func drain(recorder *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case e := <-recorder.Events:
			events = append(events, e)
		default:
			return events
		}
	}
}

// A condition is only visible to whoever opens the object; the event is what
// makes a stuck controller visible in `kubectl describe`.
func TestBecomingReadyEmitsANormalEvent(t *testing.T) {
	obj := target()
	var conditions []metav1.Condition
	recorder := record.NewFakeRecorder(10)

	SetReady(recorder, obj, &conditions, 1, true, "Accepted", "everything resolved")

	events := drain(recorder)
	if len(events) != 1 {
		t.Fatalf("expected one event, got %v", events)
	}
	if !strings.HasPrefix(events[0], corev1.EventTypeNormal) {
		t.Errorf("expected a Normal event, got %q", events[0])
	}
	if !strings.Contains(events[0], "Accepted") {
		t.Errorf("the event must carry the reason, got %q", events[0])
	}
}

func TestBecomingNotReadyEmitsAWarning(t *testing.T) {
	obj := target()
	var conditions []metav1.Condition
	recorder := record.NewFakeRecorder(10)

	SetReady(recorder, obj, &conditions, 1, false, "SecretNotFound", "the credential is missing")

	events := drain(recorder)
	if len(events) != 1 {
		t.Fatalf("expected one event, got %v", events)
	}
	if !strings.HasPrefix(events[0], corev1.EventTypeWarning) {
		t.Errorf("expected a Warning event, got %q", events[0])
	}
}

// Re-reporting the same verdict every reconcile would bury every other event
// in the object's history.
func TestAnUnchangedVerdictEmitsNothing(t *testing.T) {
	obj := target()
	var conditions []metav1.Condition
	recorder := record.NewFakeRecorder(10)

	SetReady(recorder, obj, &conditions, 1, true, "Accepted", "ok")
	drain(recorder)

	SetReady(recorder, obj, &conditions, 2, true, "Accepted", "ok")

	if events := drain(recorder); len(events) != 0 {
		t.Errorf("expected no event for an unchanged verdict, got %v", events)
	}
}

// The derived conditions explain the aggregate one; emitting for each would
// triple the event stream to say the same thing.
func TestOnlyTheAggregateConditionEmits(t *testing.T) {
	obj := target()
	var conditions []metav1.Condition
	recorder := record.NewFakeRecorder(10)

	ReportCondition(recorder, obj, &conditions, metav1.Condition{
		Type:   "RefsResolved",
		Status: metav1.ConditionFalse,
		Reason: "ReferenceNotFound",
	})

	if events := drain(recorder); len(events) != 0 {
		t.Errorf("expected no event for a derived condition, got %v", events)
	}
	// The condition is still recorded.
	if len(conditions) != 1 {
		t.Errorf("expected the condition to be set, got %v", conditions)
	}
}

// Tests build reconcilers without a manager, so the recorder is nil.
func TestANilRecorderStillRecordsTheCondition(t *testing.T) {
	obj := target()
	var conditions []metav1.Condition

	SetReady(nil, obj, &conditions, 1, true, "Accepted", "ok")

	if len(conditions) != 1 || conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("expected the condition to be recorded, got %v", conditions)
	}
}
