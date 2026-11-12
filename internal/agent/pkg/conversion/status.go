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

package conversion

import "github.com/onexstack/onexai/internal/agent/model"

// StatusToV1 normalizes a row's status to the two values the REST contract and
// the console understand.
//
// The control-plane store spells "enabled" as the empty string: that is an
// internal row spelling inherited from the legacy SQL tables. model.Enabled()
// defines enabled as "status != StatusDisabled", so anything not explicitly
// disabled is enabled on the wire. Normalizing here fixes every resource at one
// boundary and leaves the store spelling (and its round-trip tests) untouched.
func StatusToV1(status string) string {
	if status == model.StatusDisabled {
		return model.StatusDisabled
	}
	return model.StatusEnabled
}
