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
)

// Reference names another object of this API group.
type Reference struct {
	Name      string
	Namespace string
}

// CommonSpec holds the fields every resource in this group shares.
type CommonSpec struct {
	Enabled     bool
	DisplayName string
	Description string
	Owner       *rbacv1.Subject
}

// CommonStatus holds the fields every resource in this group reports.
type CommonStatus struct {
	ObservedGeneration int64
	Conditions         []metav1.Condition
}

// Condition types shared by the resources in this group.
const (
	ConditionReady        = "Ready"
	ConditionRefsResolved = "RefsResolved"
)
