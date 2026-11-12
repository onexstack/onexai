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

package agent

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AuthorizationRule is the internal (hub) schema for the authorizationrules API.
type AuthorizationRule struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   AuthorizationRuleSpec
	Status AuthorizationRuleStatus
}

// AuthorizationRuleSpec defines the desired state of AuthorizationRule.
type AuthorizationRuleSpec struct {
	Subjects         []rbacv1.Subject
	SubjectNamespace string
	AgentNames       []string
	AgentSelector    *metav1.LabelSelector
	Resource         AuthorizationResource
	Resources        []AuthorizationResource
	Verbs            []string
	Effect           AuthorizationEffect
	Attributes       *runtime.RawExtension
	Enabled          bool
	Description      string
}

// AuthorizationResource identifies what a rule addresses.
type AuthorizationResource struct {
	Kind  string
	Names []string
}

// AuthorizationEffect is the outcome a matching rule asks for.
type AuthorizationEffect string

const (
	// EffectAllow permits the request when no Deny matches.
	EffectAllow AuthorizationEffect = "Allow"
	// EffectDeny refuses the request.
	EffectDeny AuthorizationEffect = "Deny"
)

// AuthorizationRuleStatus defines the observed state of AuthorizationRule.
type AuthorizationRuleStatus struct {
	ObservedGeneration int64
	MatchedAgents      int32
	Conditions         []metav1.Condition
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AuthorizationRuleList is a list of AuthorizationRule objects.
type AuthorizationRuleList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []AuthorizationRule
}
