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

package chat

import "time"

// Snapshot lifetimes for the agent-command resolver.
//
// They live here rather than in the resolver so that pkg/options can name the
// configured defaults without importing an internal service package (a public
// package must not depend on internal/). The resolver imports these values for
// its own fallback, so the configured default and the fallback cannot drift.
const (
	// DefaultRulesTTL bounds how long a rule change can go unnoticed on a
	// replica that did not serve the write. It is short because the edit is
	// followed immediately by someone testing it.
	DefaultRulesTTL = 5 * time.Second
	// DefaultSkillsTTL bounds how long an agent's skill list can be stale. It
	// is the longest because enumerating skills is the most expensive read —
	// it can reach a skill repository — and the answer changes least.
	DefaultSkillsTTL = 2 * time.Minute
	// DefaultCustomsTTL bounds how long an agent's user-defined command list can
	// be stale. It is short because binding a command is followed immediately by
	// someone testing it, and the read is a cheap control-plane call.
	DefaultCustomsTTL = 15 * time.Second
)
