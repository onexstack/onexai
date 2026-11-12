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

package chat

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DutyGroup is the internal (hub) schema for the dutygroups API.
//
// Deprecated: see the v1alpha1 type. Duty groups are served from chat's SQL
// tables, not from this API.
type DutyGroup struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   DutyGroupSpec
	Status DutyGroupStatus
}

// DutyGroupSpec defines the desired state of DutyGroup.
type DutyGroupSpec struct {
	CommonSpec

	Agents             []string
	ScheduleRef        *Reference
	ScheduleID         string
	AllocationStrategy string
	WelcomeMessage     string
	Order              int32
	SplitByBizLine     bool
	WeeklyReport       bool
}

// DutyGroupStatus defines the observed state of DutyGroup.
type DutyGroupStatus struct {
	CommonStatus

	RegenScheduleID string
	OnCall          []OnCallMember
}

// OnCallMember is one person currently on duty.
type OnCallMember struct {
	Name  string
	Until *metav1.Time
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DutyGroupList is a list of DutyGroup objects.
type DutyGroupList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []DutyGroup
}
