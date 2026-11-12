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

package handler

import (
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// agentComposition maps the protocol message onto the run contract.
//
// Both browser run surfaces (admin and user) share this conversion: the fields
// mean the same thing on each, and only the subject differs. Normalizing here
// means the digest used for the session bucket and the names the run uses are
// the same list.
func agentComposition(in *v1.AgentComposition) *agentapi.AgentComposition {
	if in == nil {
		return nil
	}
	return (&agentapi.AgentComposition{
		Prompts:        in.GetPrompts(),
		Skills:         in.GetSkills(),
		MCPs:           in.GetMcps(),
		KnowledgeBases: in.GetKnowledgeBases(),
		CustomCommands: in.GetCustomCommands(),
		Model:          in.GetModel(),
		Namespace:      in.GetNamespace(),
	}).Normalized()
}

// composeSessionID derives the memory bucket for one composed debug run.
//
// The prefix differs by surface ("dbg" for the management debug run, "usr" for
// the self-service one) and the token after it is opaque; see
// agentapi.ComposeSessionID for why a readable tuple cannot be used.
func composeSessionID(
	prefix, agent, subjectID, actorID string, in *v1.AgentComposition, nonce string,
) string {
	return agentapi.ComposeSessionID(prefix, agent, subjectID, actorID, agentComposition(in), nonce)
}
