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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Skill is the internal (hub) schema for the skills API.
type Skill struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   SkillSpec
	Status SkillStatus
}

// SkillSpec defines the desired state of Skill.
type SkillSpec struct {
	CommonSpec

	Provider        string
	Params          *runtime.RawExtension
	Mode            string
	CacheTTLSeconds int32
	Contract   string
	Entrypoint        string
}

// SkillStatus defines the observed state of Skill.
type SkillStatus struct {
	CommonStatus

	SyncedRevision string
	Digest         string
	LastSyncTime   *metav1.Time
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SkillList is a list of Skill objects.
type SkillList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []Skill
}
