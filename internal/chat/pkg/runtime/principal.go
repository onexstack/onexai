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

package runtime

import "github.com/onexstack/onexai/internal/chat/model"

// SubjectFor returns the identity the agent service authorizes a speaker as: the
// platform account their Lark identity is bound to, or their tenant user_id when
// there is no binding.
//
// # Why this is one function
//
// The agent service evaluates its rules against the Subject it is handed, so the
// choice of which string that is belongs to this side, where the binding table
// lives. Two callers need it: the scheduler, which puts it into the ask a run is
// authorized by, and /agent, which asks the same question in advance so a
// mistyped or forbidden agent is refused where it was typed rather than on the
// next message. They have to agree — a preview that named a different identity
// would refuse an agent the run itself allows, which reads to the speaker as
// "you may not use this" about an agent they were using.
//
// A nil binding, or one that carries no canonical identity, is the tenant
// identity: that is what rules for internal callers are keyed by. It is empty
// for an external speaker, and an empty subject is refused by the agent service
// — the visible failure rather than a guessed one.
func SubjectFor(tenantUserID string, binding *model.PeerAgentM) string {
	if binding != nil && binding.Canonical != "" {
		return binding.Canonical
	}
	return tenantUserID
}
