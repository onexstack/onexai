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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DutyGroup is one on-call group (a duty group). Its spec is what the group should be;
// the roster itself lives in Regen, so status carries what this platform last
// read back from there.
//
// Deprecated: the chat runtime no longer reads or writes DutyGroup objects. Duty
// groups are a chat-owned SQL resource (chat.oncall_group and its schedule
// tables). The type stays served so existing objects can be inspected or
// migrated; do not build new dependencies on it.
type DutyGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec DutyGroupSpec `json:"spec,omitempty"`
	// +optional
	Status DutyGroupStatus `json:"status,omitempty"`
}

// DutyGroupSpec defines the desired state of DutyGroup.
type DutyGroupSpec struct {
	CommonSpec `json:",inline"`

	// Agents names the agents available to this group, by their reference
	// syntax: "@peer", "@oncall", "@default", or a bare agent name.
	Agents []string `json:"agents"`

	// ScheduleRef names the Regen schedule supplying the roster. When set, the
	// controller resolves it and reports the id in status.
	// +optional
	ScheduleRef *Reference `json:"scheduleRef,omitempty"`

	// ScheduleID is the Regen schedule id used directly. It wins over
	// ScheduleRef when both are set.
	// +optional
	ScheduleID string `json:"scheduleID,omitempty"`

	// AllocationStrategy is how a ticket picks its assignee.
	// +optional
	AllocationStrategy string `json:"allocationStrategy,omitempty"`

	// WelcomeMessage is posted when the group's chat is created.
	// +optional
	WelcomeMessage string `json:"welcomeMessage,omitempty"`

	// Order positions the group in listings.
	// +optional
	Order int32 `json:"order,omitempty"`

	// SplitByBizLine makes allocation per business line.
	// +optional
	SplitByBizLine bool `json:"splitByBizLine,omitempty"`

	// WeeklyReport asks for a weekly summary.
	// +optional
	WeeklyReport bool `json:"weeklyReport,omitempty"`
}

// DutyGroupStatus defines the observed state of DutyGroup.
type DutyGroupStatus struct {
	CommonStatus `json:",inline"`

	// RegenScheduleID is the schedule the roster was read from.
	// +optional
	RegenScheduleID string `json:"regenScheduleID,omitempty"`

	// OnCall is the roster as of the last successful read.
	// +optional
	OnCall []OnCallMember `json:"onCall,omitempty"`
}

// OnCallMember is one person currently on duty.
type OnCallMember struct {
	Name string `json:"name"`
	// +optional
	Until *metav1.Time `json:"until,omitempty"`
}

// DutyGroup condition types beyond the shared ones.
const (
	// ConditionRosterReady reports that a roster was read from Regen.
	ConditionRosterReady = "RosterReady"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DutyGroupList is a list of DutyGroup objects.
type DutyGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []DutyGroup `json:"items"`
}
