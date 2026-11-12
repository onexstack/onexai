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

package agent

import (
	"fmt"
	"strings"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// CapabilitySelectionError refuses a selection naming a capability the agent
// does not have.
//
// It is exported because the handler maps it to 400: the caller can fix the
// request, and telling them which name was rejected is the whole point of
// refusing instead of silently dropping it.
type CapabilitySelectionError struct {
	// Unknown is the rejected names, each rendered as `skill "name"`.
	Unknown []string
}

func (e *CapabilitySelectionError) Error() string {
	return "capability selection is not part of this agent's effective capabilities: " +
		strings.Join(e.Unknown, ", ")
}

// applyCapabilitySelection narrows a resolved run to the capabilities the
// caller asked for.
//
// # Why it narrows the spec rather than the request downstream
//
// Everything the run derives from the selection — RunOptions, the capability
// token, the revision digest, the tool set — is read off sel.spec after this
// point. Filtering each of those call sites instead would leave one of them
// reading the full set the day someone adds another.
//
// # What it deliberately does not touch
//
// Approval patterns stay the full union. They are a safety floor, not a
// capability: dropping patterns because an MCP was deselected would turn a
// narrowing request into a way to disable a gate. The only effect of keeping
// them is that an uncallable tool may still be listed, which over-gates.
//
// Delegates, session tools, memory and webhooks are outside the selector for
// the same reason — none of them is something a test run is expected to
// toggle, and each has its own authorization story.
//
// # strict
//
// Ask passes true: a name outside the effective set means the menu and the run
// are looking at different declarations, and answering anyway would run
// something the person did not select.
//
// Approve passes false: the names came off the checkpoint, and a resource
// revoked while the run waited must be dropped rather than make the pause
// unanswerable. Intersecting is the only safe direction there; it can never
// widen access.
func applyCapabilitySelection(
	sel *resolvedSelection, want *agentapi.CapabilitySelection, strict bool,
) error {
	if want == nil {
		return nil
	}

	if strict {
		var unknown []string
		unknown = append(unknown, unknownCapabilities("skill", sel.spec.Skills, want.Skills)...)
		unknown = append(unknown, unknownCapabilities("mcp", sel.spec.MCPs, want.MCPs)...)
		unknown = append(unknown, unknownCapabilities("knowledge base", sel.spec.KnowledgeBases, want.KnowledgeBases)...)
		unknown = append(unknown, unknownCapabilities("custom command", sel.spec.CustomCommands, want.CustomCommands)...)
		if len(unknown) > 0 {
			return &CapabilitySelectionError{Unknown: unknown}
		}
	}

	sel.spec.Skills = retainCapabilities(sel.spec.Skills, want.Skills)
	sel.spec.MCPs = retainCapabilities(sel.spec.MCPs, want.MCPs)
	sel.spec.KnowledgeBases = retainCapabilities(sel.spec.KnowledgeBases, want.KnowledgeBases)
	sel.spec.CustomCommands = retainCapabilities(sel.spec.CustomCommands, want.CustomCommands)
	sel.narrowed = true
	return nil
}

// retainCapabilities keeps the names in allowed that the selection names,
// preserving allowed's order.
//
// Order comes from the resolved set, not the request: the spec order is the
// one the rest of the run was built from, and two callers naming the same set
// in different orders should produce the same revision digest.
//
// An empty selection yields nil, which is "none of this kind". That is why the
// caller's message presence is the switch — this function cannot tell "none"
// from "not narrowed".
func retainCapabilities(allowed, requested []string) []string {
	if len(requested) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		want[name] = struct{}{}
	}
	out := make([]string, 0, len(allowed))
	for _, name := range allowed {
		if _, ok := want[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// unknownCapabilities names the requested capabilities the agent does not
// have. A blank name is reported too: it names nothing, so it cannot be part
// of an effective set.
func unknownCapabilities(kind string, allowed, requested []string) []string {
	if len(requested) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		have[name] = struct{}{}
	}
	var unknown []string
	seen := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if _, ok := have[name]; !ok {
			unknown = append(unknown, fmt.Sprintf("%s %q", kind, name))
		}
	}
	return unknown
}
