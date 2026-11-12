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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ScheduledRun is the internal (hub) schema for scheduled agent runs.
//
// Deprecated: use a batch.onex.ai CronJob with an agentrun jobTemplate. The
// /v1/agent/scheduledruns facade remains the supported public API.
type ScheduledRun struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   ScheduledRunSpec
	Status ScheduledRunStatus
}

// ScheduledRunSpec defines one cron schedule. Every occurrence creates a new
// session; the schedule itself is the only durable identity.
type ScheduledRunSpec struct {
	CommonSpec

	Agent            Reference
	Schedule         string
	TimeZone         string
	Prompt           string
	Paused           bool
	ExecutionTimeout *metav1.Duration
}

// ScheduledRunStatus reports the last execution the scheduler started.
type ScheduledRunStatus struct {
	CommonStatus

	LastExecutionID string
	LastScheduledAt *metav1.Time
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ScheduledRunList is a list of ScheduledRun objects.
type ScheduledRunList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []ScheduledRun
}
