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

package agent

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// The main resource endpoint owns spec and metadata; /status owns only status.
// A client that writes /status must not be able to cancel a deletion, reparent
// the object away from the controller that created it, or relabel it.
func TestStatusStrategyRestoresStoredMetadata(t *testing.T) {
	deletedAt := metav1.NewTime(time.Now().Add(-time.Hour))
	owner := metav1.OwnerReference{APIVersion: "agent.onexai.io/v1alpha1", Kind: "Agent", Name: "owner"}

	stored := &agent.Agent{}
	stored.Name = "a"
	stored.Labels = map[string]string{"keep": "me"}
	stored.DeletionTimestamp = &deletedAt
	stored.OwnerReferences = []metav1.OwnerReference{owner}
	stored.Spec.DisplayName = "stored"

	// What a caller submits to /status: everything reset, plus a new condition.
	update := &agent.Agent{}
	update.Name = "a"
	update.Labels = map[string]string{"tampered": "yes"}
	update.Spec.DisplayName = "tampered"
	update.Status.Conditions = []metav1.Condition{{
		Type:               agent.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Healthy",
		LastTransitionTime: metav1.NewTime(time.Now()),
	}}

	StatusStrategy.PrepareForUpdate(context.Background(), update, stored)

	if update.Spec.DisplayName != "stored" {
		t.Errorf("spec was not restored: got %q", update.Spec.DisplayName)
	}
	if got := update.Labels["keep"]; got != "me" {
		t.Errorf("labels were not restored: %v", update.Labels)
	}
	if _, tampered := update.Labels["tampered"]; tampered {
		t.Error("a /status write must not add labels")
	}
	if len(update.OwnerReferences) != 1 || update.OwnerReferences[0].Name != "owner" {
		t.Errorf("ownerReferences were not restored: %v", update.OwnerReferences)
	}
	if update.DeletionTimestamp == nil || !update.DeletionTimestamp.Equal(&deletedAt) {
		t.Error("a /status write must not cancel a pending deletion")
	}
	if len(update.Status.Conditions) != 1 {
		t.Errorf("status must be writable through the status subresource, got %v", update.Status.Conditions)
	}
}

// A /status write against a live object must not be able to introduce a
// deletion either, which a plain "copy old back" would not catch on its own.
func TestStatusStrategyCannotStartDeletion(t *testing.T) {
	now := metav1.NewTime(time.Now())
	stored := &agent.Agent{}
	stored.Name = "a"

	update := &agent.Agent{}
	update.Name = "a"
	update.DeletionTimestamp = &now

	StatusStrategy.PrepareForUpdate(context.Background(), update, stored)

	if update.DeletionTimestamp != nil {
		t.Error("a /status write must not set deletionTimestamp")
	}
}

func TestStatusStrategyValidatesConditions(t *testing.T) {
	stored := &agent.Agent{}
	stored.Name = "a"

	update := &agent.Agent{}
	update.Name = "a"
	// Reason is required whenever status is True or False.
	update.Status.Conditions = []metav1.Condition{{
		Type:               agent.ConditionReady,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}}

	if errs := StatusStrategy.ValidateUpdate(context.Background(), update, stored); len(errs) == 0 {
		t.Error("expected a missing condition reason to be rejected")
	}
}

func TestStatusStrategyRejectsNegativeObservedGeneration(t *testing.T) {
	stored := &agent.Agent{}
	stored.Name = "a"

	update := &agent.Agent{}
	update.Name = "a"
	update.Status.ObservedGeneration = -1

	errs := StatusStrategy.ValidateUpdate(context.Background(), update, stored)
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error, got %v", errs)
	}
	if errs[0].Field != "status.observedGeneration" {
		t.Errorf("unexpected field path %q", errs[0].Field)
	}
}
