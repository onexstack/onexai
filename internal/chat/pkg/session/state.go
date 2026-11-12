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

package session

import "time"

// Backend names. These mirror options.AgentBackendLLM/CLI but are duplicated as
// plain strings so this package owns no configuration dependency.
const (
	BackendLLM = "llm"
	BackendCLI = "cli"
)

// Platform is the messaging platform a conversation belongs to. There is only
// one today; the field exists so snapshots stay readable when there are more.
const PlatformLark = "lark"

// Mode values. State.Mode is a free-form label the backend-side code interprets;
// the reasoning preference is the one meaning defined today. An empty Mode is
// the default, which is reasoning on — the behaviour this service had before the
// preference was stored at all.
const (
	// ModeReasoningOn asks the agent to surface its reasoning stream.
	ModeReasoningOn = "reasoning-on"
	// ModeReasoningOff keeps it hidden.
	ModeReasoningOff = "reasoning-off"
)

// State is the persisted per-conversation state.
//
// The boundary is deliberate and narrow: this holds the *agent binding* — which
// backend runs the conversation, where it runs, and how to resume it — plus
// bookkeeping timestamps. Conversation history is not here and never will be:
// it belongs to oai-agent-apiserver. Keeping history out means the two stores can
// never disagree about what was said, only about which backend is attached.
//
// There is no lock field. See Lock for why.
type State struct {
	// Key identifies the conversation within its platform.
	Key string `json:"key"`
	// Platform is the messaging platform (PlatformLark).
	Platform string `json:"platform"`
	// Backend is BackendLLM or BackendCLI.
	Backend string `json:"backend"`
	// WorkDir is the working directory handed to a CLI backend, set by /dir. An
	// empty value is the default: the conversation's own workspace under the
	// workspace root. A set value has been confined to one of the backend's
	// allowed roots when it was accepted, and is confined again when the turn
	// runs, so a snapshot that predates a configuration change cannot point a
	// process outside the current roots.
	WorkDir string `json:"work_dir,omitempty"`
	// BackendSessionID is the identifier the backend uses to resume this
	// conversation (the CLI's --resume target). It stays empty for the LLM
	// backend on purpose: agent already owns that conversation, so keeping
	// this empty makes the two stores' disagreement detectable rather than
	// possible.
	BackendSessionID string `json:"backend_session_id,omitempty"`
	// Mode is a free-form conversation mode label that the backend-side code
	// interprets as it sees fit. Today it carries the reasoning preference
	// (ModeReasoningOn / ModeReasoningOff); an empty value means the default.
	//
	// It is here rather than in a per-request map because it is a preference the
	// user set, not a property of one turn: it should survive a restart with the
	// conversation, and /new — which clears the binding — deliberately keeps it.
	Mode string `json:"mode,omitempty"`
	// LastUserActivity is when the user last sent something to this
	// conversation. Used to reap idle conversations.
	LastUserActivity time.Time `json:"last_user_activity"`
	// CreatedAt is when the snapshot was first written.
	CreatedAt time.Time `json:"created_at"`
}

// Clone returns a copy. State is a flat value type, so this is a plain copy
// that exists to make the intent at call sites explicit: callers get a snapshot
// that cannot be mutated out from under the session.
func (s State) Clone() State { return s }
