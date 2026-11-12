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

// The resource namespace used to be spelled "workspace" on this API, because
// the declarative store's namespace was presented as a business-line workspace.
// It is the same field, so the rename is a vocabulary change rather than a data
// change — but it is a breaking one for anything that reads the JSON.
//
// Both spellings therefore travel for one release: a request may carry either
// (namespace wins when both are set), and every response carries both, so a
// client built before the rename keeps reading the value it expects. The next
// release drops the deprecated field, at which point these two helpers go away
// with it.

// RequestedNamespace returns the namespace a request named, preferring the new
// spelling and falling back to the deprecated one.
func RequestedNamespace(namespace, workspace string) string {
	if namespace != "" {
		return namespace
	}
	return workspace
}

// LegacyWorkspace returns the deprecated field's value for a response: the same
// namespace under the old spelling. It exists to leave the reason at the call
// site, so the assignment does not read as a copy/paste slip.
func LegacyWorkspace(namespace string) string { return namespace }
