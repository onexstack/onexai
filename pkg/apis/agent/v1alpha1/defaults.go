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

import "github.com/onexstack/onexstack/pkg/ptr"

// SetDefaults_AgentBinding makes a binding active unless it says otherwise.
//
// Enabled is a pointer precisely so this default is safe: a plain bool cannot
// distinguish "not written" from "explicitly false", and defaulting a plain
// bool to true would override the intent of a disabled binding.
func SetDefaults_AgentBinding(obj *AgentBinding) {
	if obj.Spec.Enabled == nil {
		obj.Spec.Enabled = ptr.To(true)
	}
}
