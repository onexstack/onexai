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

package v1beta1

// JobTypeAgentRun is the Job type whose provider starts an agent run through
// oai-agent-apiserver's asynchronous run API. It is the type ScheduledRun is
// migrated to.
const JobTypeAgentRun JobType = "agentrun"

// AgentRunProviderSpec is the typed payload carried in Job.Spec.ProviderSpec
// when Job.Spec.Type is JobTypeAgentRun. It is a plain JSON contract rather than
// a registered Kind: the core Job API stays provider-neutral, while clients and
// the agentrun provider share one schema.
type AgentRunProviderSpec struct {
	// Agent names the agent to run.
	Agent string `json:"agent"`
	// Prompt is the user message the run starts with.
	Prompt string `json:"prompt"`
	// SubjectID is the identity the run executes as. Authorization, quota and
	// attribution all use it.
	SubjectID string `json:"subjectID"`
	// SubjectName is the display name of the subject.
	// +optional
	SubjectName string `json:"subjectName,omitempty"`
	// Metering says who writes the run's ledger rows: "none", "runtime" or
	// "caller". Empty means the agent server's default.
	// +optional
	Metering string `json:"metering,omitempty"`
	// ApprovalFallback is what a gated tool does when the run cannot pause for
	// a human. Scheduled jobs should use "deny".
	// +optional
	ApprovalFallback string `json:"approvalFallback,omitempty"`
	// SessionPolicy is "new" (a fresh session per occurrence) or "reuse".
	// +optional
	SessionPolicy string `json:"sessionPolicy,omitempty"`
	// Source names the surface that started the run, for audit attribution.
	// CronJob-created jobs use "scheduled-run".
	// +optional
	Source string `json:"source,omitempty"`
	// TimeoutSeconds bounds one run. Zero means the agent server's default.
	// +optional
	TimeoutSeconds *int64 `json:"timeoutSeconds,omitempty"`
}

// AgentRunProviderStatus is the typed payload carried in Job.Status.ProviderStatus
// when Job.Spec.Type is JobTypeAgentRun.
type AgentRunProviderStatus struct {
	// RunID is the agent run this Job dispatched.
	// +optional
	RunID string `json:"runID,omitempty"`
	// SessionID is the conversation the run belongs to.
	// +optional
	SessionID string `json:"sessionID,omitempty"`
	// Attempt is the zero-based retry attempt this RunID belongs to.
	// +optional
	Attempt int32 `json:"attempt,omitempty"`
	// Phase mirrors the agent run state for observability.
	// +optional
	Phase string `json:"phase,omitempty"`
	// Message carries a terminal failure reason or a short result summary.
	// +optional
	Message string `json:"message,omitempty"`
}
