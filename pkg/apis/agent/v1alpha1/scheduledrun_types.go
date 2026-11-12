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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ScheduledRun runs an agent on a five-field cron schedule. Each occurrence
// creates a fresh Session; task state and history belong to the run store.
//
// Deprecated: schedules are batch.onex.ai CronJobs whose jobTemplate has
// spec.type agentrun. The /v1/agent/scheduledruns REST facade remains supported
// and reads/writes those CronJobs; this object has no writer left. The type
// stays served so oai-scheduledrun-migrator can read pre-migration objects.
type ScheduledRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ScheduledRunSpec `json:"spec,omitempty"`
	// +optional
	Status ScheduledRunStatus `json:"status,omitempty"`
}

// ScheduledRunSpec defines one schedule.
type ScheduledRunSpec struct {
	CommonSpec `json:",inline"`

	// Agent names the agent to run, in this namespace.
	Agent Reference `json:"agent"`

	// Schedule is a five-field cron expression.
	Schedule string `json:"schedule"`

	// TimeZone is an IANA name. Empty means UTC.
	// +optional
	TimeZone string `json:"timeZone,omitempty"`

	// Prompt is the user message each occurrence starts with.
	Prompt string `json:"prompt"`

	// Paused suspends new occurrences without deleting the schedule.
	// +optional
	Paused bool `json:"paused,omitempty"`

	// ExecutionTimeout bounds one occurrence, including queueing and
	// provisioning. Nil means 15 minutes.
	// +optional
	ExecutionTimeout *metav1.Duration `json:"executionTimeout,omitempty"`
}

// ScheduledRunStatus reports the last execution the scheduler started.
type ScheduledRunStatus struct {
	CommonStatus `json:",inline"`

	// LastExecutionID is the most recent execution row.
	// +optional
	LastExecutionID string `json:"lastExecutionId,omitempty"`

	// LastScheduledAt is when that execution was admitted.
	// +optional
	LastScheduledAt *metav1.Time `json:"lastScheduledAt,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ScheduledRunList is a list of ScheduledRun objects.
type ScheduledRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ScheduledRun `json:"items"`
}
