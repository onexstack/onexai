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

// Reference names another object of this API group.
//
// The kind is implied by the field that carries the reference (skills hold
// Skills, knowledgeBases hold KnowledgeBases), so it is not repeated here: a
// kind field could only ever disagree with the field name.
//
// Namespace is optional. Empty means the referrer's namespace; a non-empty value
// addresses an object in another namespace, which the RBAC of the referrer's
// namespace must allow.
type Reference struct {
	// Name is the referenced object's name.
	Name string `json:"name"`

	// Namespace is the referenced object's namespace. Empty means the same
	// namespace as the referrer.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// CommonSpec holds the fields every resource in this group shares. Embedding it
// keeps displayName/description/owner from being redefined seven times.
type CommonSpec struct {
	// Enabled toggles the resource without deleting it. A disabled object is
	// invisible to resolution, which is the declarative way to withdraw it while
	// keeping its history.
	// +optional
	Enabled bool `json:"enabled"`

	// DisplayName is the human-facing name.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Description explains what the object is for.
	// +optional
	Description string `json:"description,omitempty"`

	// Owner is the principal this object belongs to. It is declarative — the
	// immutable record of who created the object is the onexai.io/created-by
	// annotation, written by admission from the authenticated user.
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

// AnnotationCreatedBy records the authenticated principal that created the
// object. Unlike spec.owner it is immutable: admission writes it once and the
// update strategy preserves the stored value.
const AnnotationCreatedBy = "onexai.io/created-by"

// Condition types shared by the resources in this group.
const (
	// ConditionReady is the aggregate condition: true when every other
	// condition is true and the object can be used.
	ConditionReady = "Ready"
	// ConditionRefsResolved reports that every reference the object names
	// exists and is enabled.
	ConditionRefsResolved = "RefsResolved"
)
