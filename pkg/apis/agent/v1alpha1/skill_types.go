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
	"k8s.io/apimachinery/pkg/runtime"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Skill is a capability package fetched from an external source (GitHub,
// GitLab, a Lark document, a skill hub). The controller resolves and validates
// it, so the runtime reads a revision instead of downloading at request time.
type Skill struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec SkillSpec `json:"spec,omitempty"`
	// +optional
	Status SkillStatus `json:"status,omitempty"`
}

// SkillSpec defines the desired state of Skill.
type SkillSpec struct {
	CommonSpec `json:",inline"`

	// Provider is the skill source: github, gitlab or lark_doc.
	Provider string `json:"provider"`

	// Params is the provider-specific configuration (repository, path, ref…).
	// +optional
	Params *runtime.RawExtension `json:"params,omitempty"`

	// Mode decides what a client receives: hosted loads the body into the
	// server-side agent, tool runs Entrypoint, guide returns only Contract.
	// +optional
	Mode string `json:"mode,omitempty"`

	// CacheTTLSeconds bounds how long a resolved revision may be reused.
	// +optional
	CacheTTLSeconds int32 `json:"cacheTTLSeconds,omitempty"`

	// Contract is the compact description a local model needs to delegate.
	// +optional
	Contract string `json:"contract,omitempty"`

	// Entrypoint is the executable a "tool" skill runs.
	// +optional
	Entrypoint string `json:"entrypoint,omitempty"`
}

// SkillStatus defines the observed state of Skill.
type SkillStatus struct {
	CommonStatus `json:",inline"`

	// SyncedRevision is the revision the controller last resolved.
	// +optional
	SyncedRevision string `json:"syncedRevision,omitempty"`

	// Digest is the content digest of that revision.
	// +optional
	Digest string `json:"digest,omitempty"`

	// LastSyncTime is when the source was last fetched successfully.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`
}

// Skill condition types beyond the shared ones.
const (
	// ConditionSourceReachable reports that the provider answered.
	ConditionSourceReachable = "SourceReachable"
	// ConditionSynced reports that the current revision has been fetched.
	ConditionSynced = "Synced"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SkillList is a list of Skill objects.
type SkillList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Skill `json:"items"`
}
