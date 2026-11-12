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

package v1alpha1

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CommonSpec holds the fields every resource in this group shares.
type CommonSpec struct {
	// Enabled toggles the resource without deleting it.
	// +optional
	Enabled bool `json:"enabled"`

	// DisplayName is the human-facing name.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Description explains what the object is for.
	// +optional
	Description string `json:"description,omitempty"`

	// Owner is the principal this object belongs to.
	// +optional
	Owner *rbacv1.Subject `json:"owner,omitempty"`
}

// CommonStatus holds the fields every resource in this group reports.
type CommonStatus struct {
	// ObservedGeneration is the generation the controller last acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions describe the resource's health.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Reference names another object of this API group.
type Reference struct {
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// Condition types shared by the resources in this group.
const (
	ConditionReady            = "Ready"
	ConditionAgentsResolved   = "AgentsResolved"
	ConditionScheduleResolved = "ScheduleResolved"
)
