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
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/prompttmpl"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authz"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

// CompositionError refuses a composition naming a resource that cannot be used.
//
// It is exported because the handler maps it to 400: the caller can fix the
// request, and naming the rejected resources is what turns "400" into an
// actionable message. A composition is a set of names the user picked from a
// catalog, so a name that no longer resolves is a stale menu, not a server
// fault.
type CompositionError struct {
	// Rejected names each resource as `prompt "x" (missing)`, `skill "y"
	// (disabled)`, and so on.
	Rejected []string
	// Message is set for failures that are not about a named resource, such as
	// a namespace-policy refusal.
	Message string
}

func (e *CompositionError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "composition references resources that cannot be used: " + strings.Join(e.Rejected, ", ")
}

// composedPromptCount is for the run-start log line: the number of prompt
// resources, never their content.
func composedPromptCount(c *agentapi.AgentComposition) int {
	if c == nil {
		return 0
	}
	return len(c.Prompts)
}

// applyComposition replaces the base selection's instruction and capability
// lists with the composition's. The base selection may be empty: a hostless
// playground run starts from nothing and the composition is the whole
// configuration.
//
// # Why it is a replacement rather than a filter
//
// The console's debug surface lets a user try a combination the host agent does
// not declare. Everything the run derives from the selection — RunOptions, the
// capability token, the revision digest, the tool set — is read off sel.spec
// after this point, so replacing here is the one place that keeps them
// consistent. It also marks the selection narrowed, which bypasses the runner
// cache (built from agent names) and makes the remote capability token carry
// these exact lists.
//
// # What it deliberately leaves alone
//
// Delegates, session tools, memory settings, webhooks and the runtime profile
// are host properties, not capabilities: a debug run is showing what this host
// does with a different skill/tool/prompt set, not creating a second host. The
// approval patterns are a safety floor: host patterns and server-wide patterns
// stay, and the composition can only add the MCP servers it names.
//
// # strict
//
// Ask passes true: a name that does not resolve means the menu and the run are
// looking at different catalogs, and running anyway would use something the
// person did not select.
//
// Approve passes false: the names came off the checkpoint, and a resource
// revoked while the run waited must be dropped rather than make the pause
// unanswerable. Dropping can only narrow, never widen.
func (b *AgentBiz) applyComposition(
	ctx context.Context, sel *resolvedSelection, c *agentapi.AgentComposition, strict bool, referrer string,
) error {
	if c == nil {
		return nil
	}
	if b.deps.Resources == nil {
		return &CompositionError{Message: "composition is not available: the service has no resource store"}
	}
	normalized := c.Normalized()
	// Everything below works on canonical "namespace/name" entries. A bare
	// entry belongs to the referrer; resolving it once here is what keeps the
	// load, the namespace gate and the engine from each guessing a different
	// object when two namespaces hold the same name.
	referrer = sharing.Normalize(referrer)

	prompts, rejected := normalizeCompositionEntries("prompt", referrer, normalized.Prompts)
	skillRefs, rejected := normalizeCompositionEntries("skill", referrer, normalized.Skills)
	mcpRefs, rejected := normalizeCompositionEntries("mcp", referrer, normalized.MCPs)
	kbRefs, rejected := normalizeCompositionEntries("knowledge base", referrer, normalized.KnowledgeBases)
	commandRefs, rejected := normalizeCompositionEntries("custom command", referrer, normalized.CustomCommands)

	// A custom command's body runs with the caller's authority, so it is never
	// shareable: a cross-namespace entry is refused rather than checked against
	// a grant that could not make it safe.
	if err := requireSameNamespace("custom command", referrer, commandRefs); err != nil {
		if strict {
			rejected = append(rejected, err.Error())
		}
	}

	instruction, rejected := renderCompositionPrompts(ctx, b.deps.Resources, sel, prompts, strict, rejected)
	skills, rejected := loadCompositionNames(ctx, "skill", skillRefs, strict, rejected,
		func(name string) bool {
			row, err := b.deps.Resources.Skills().Get(ctx, model.EntryFilter(name))
			return err == nil && row != nil && row.Enabled()
		})
	mcps, mcpRows, rejected := loadCompositionMCPs(ctx, b.deps.Resources, mcpRefs, strict, rejected)
	kbs, rejected := loadCompositionNames(ctx, "knowledge base", kbRefs, strict, rejected,
		func(name string) bool {
			row, err := b.deps.Resources.KnowledgeBases().Get(ctx, model.EntryFilter(name))
			return err == nil && row != nil && row.Enabled()
		})
	commands, rejected := loadCompositionNames(ctx, "custom command", commandRefs, strict, rejected,
		func(name string) bool {
			row, err := b.deps.Resources.CustomCommands().Get(ctx, model.EntryFilter(name))
			return err == nil && row != nil && row.Enabled()
		})

	if len(rejected) > 0 {
		// Assigned above only to locals; the host spec is untouched.
		return &CompositionError{Rejected: rejected}
	}
	// A prompt template can include data sources, so a small selection can still
	// render a large instruction. Refusing here keeps one debug request from
	// handing the model a payload nobody intended to send.
	const maxInstructionBytes = 256 * 1024
	if len(instruction) > maxInstructionBytes {
		return &CompositionError{Message: fmt.Sprintf(
			"composition renders a %d-byte instruction; the limit is %d bytes",
			len(instruction), maxInstructionBytes,
		)}
	}

	approval := append([]string(nil), sel.spec.Approval...)
	for _, row := range mcpRows {
		approval = append(approval, row.RequireApproval...)
	}

	sel.spec.Instruction = instruction
	sel.spec.InstructionSet = true
	sel.spec.PromptRefs = prompts
	sel.spec.Skills = skills
	sel.spec.MCPs = mcps
	sel.spec.KnowledgeBases = kbs
	sel.spec.CustomCommands = commands
	sel.spec.Approval = agentapi.NormalizeNames(approval)
	sel.narrowed = true
	return nil
}

// renderCompositionPrompts renders the selected prompts in order.
//
// Prompt order is preserved because it decides the instruction's order; the
// template renderer receives the host agent's identity, which is the identity
// the run executes under.
func renderCompositionPrompts(
	ctx context.Context, resources store.AIStore, sel *resolvedSelection,
	prompts []string, strict bool, rejected []string,
) (string, []string) {
	if len(prompts) == 0 {
		return "", rejected
	}
	var host *model.AgentM
	if len(sel.set.records) > 0 {
		host = sel.set.records[0]
	}
	data := prompttmpl.Data{}
	if host != nil {
		data = prompttmpl.Data{
			AgentName:      host.Name,
			AgentNamespace: host.Namespace,
			Description:    host.Description,
		}
	}

	var parts []string
	for _, name := range prompts {
		p, err := resources.Prompts().Get(ctx, model.EntryFilter(name))
		if err != nil || p == nil || !p.Enabled() || strings.TrimSpace(p.Content) == "" {
			if strict {
				rejected = append(rejected, fmt.Sprintf("prompt %q (missing, disabled or empty)", name))
				continue
			}
			slog.WarnContext(ctx, "dropping an unavailable prompt from a resumed composition", "prompt", name)
			continue
		}
		rendered, err := prompttmpl.Render(p.Content, data, p.DataSources)
		if err != nil {
			if strict {
				rejected = append(rejected, fmt.Sprintf("prompt %q (does not render: %v)", name, err))
				continue
			}
			slog.WarnContext(ctx, "dropping a prompt that does not render from a resumed composition",
				"prompt", name, "error", err)
			continue
		}
		if strings.TrimSpace(rendered) != "" {
			parts = append(parts, rendered)
		}
	}
	return strings.Join(parts, "\n\n"), rejected
}

// loadCompositionNames resolves one capability kind, preserving request order.
//
// usable is the lookup and the enabled-state test in one call: the four store
// interfaces differ only in their row type, so a closure is what lets them
// share the reporting below. A change to how a missing or disabled resource is
// reported then cannot land on one kind and miss the others.
func loadCompositionNames(
	ctx context.Context, kind string, names []string, strict bool, rejected []string,
	usable func(name string) bool,
) ([]string, []string) {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if !usable(name) {
			if strict {
				rejected = append(rejected, fmt.Sprintf("%s %q (missing or disabled)", kind, name))
				continue
			}
			slog.WarnContext(ctx, "dropping an unavailable resource from a resumed composition",
				"kind", kind, "name", name)
			continue
		}
		out = append(out, name)
	}
	return out, rejected
}

// loadCompositionMCPs is loadCompositionNames plus the rows, because the
// approval floor is read off each selected server.
func loadCompositionMCPs(
	ctx context.Context, resources store.AIStore, names []string, strict bool, rejected []string,
) ([]string, []*model.MCPM, []string) {
	out := make([]string, 0, len(names))
	rows := make([]*model.MCPM, 0, len(names))
	for _, name := range names {
		row, err := resources.MCPs().Get(ctx, model.EntryFilter(name))
		if err != nil || row == nil || !row.Enabled() {
			if strict {
				rejected = append(rejected, fmt.Sprintf("mcp %q (missing or disabled)", name))
				continue
			}
			slog.WarnContext(ctx, "dropping an unavailable MCP server from a resumed composition", "mcp", name)
			continue
		}
		out = append(out, name)
		rows = append(rows, row)
	}
	return out, rows, rejected
}

// normalizeCompositionEntries resolves every entry against the referrer and
// renders it as "namespace/name", preserving order and deduplicating by the
// full reference.
//
// This is the one place a composition entry becomes an identity. A bare name
// belongs to the referrer; a qualified name names its own namespace. Two rows
// that share a name in different namespaces are therefore two entries, not one
// — the old bare-name dedupe is what made the console and the server disagree
// about which object was selected.
func normalizeCompositionEntries(kind, referrer string, entries []string) ([]string, []string) {
	out := make([]string, 0, len(entries))
	var rejected []string
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			rejected = append(rejected, fmt.Sprintf("%s name is empty", kind))
			continue
		}
		canonical := model.CanonicalEntry(referrer, entry)
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	return out, rejected
}

// requireSameNamespace refuses a reference that leaves the referrer's
// namespace for a kind that is not shareable.
func requireSameNamespace(kind, referrer string, entries []string) error {
	for _, entry := range entries {
		ref := model.SplitRef(entry)
		if ref.Namespace != referrer {
			return fmt.Errorf("%s %q must be in namespace %q; this kind is not shareable",
				kind, entry, referrer)
		}
	}
	return nil
}

// authorizeComposition authorizes the composed resource references.
//
// # Why the host agent's name, not its bindings
//
// A composed run replaces the host's skills, MCPs and knowledge bases, so the
// question is whether this subject may use *these* resources in this agent's
// context. Checking the host's declarations instead would refuse a valid
// composition because of a resource it removed, and would allow one whose
// resources were never declared.
//
// # What is not checked
//
// Prompts and custom commands have no authz resource kind today; they are
// governed by the agent gate and the capability token respectively. Their
// content is readable by any signed-in user (the declarative surface treats
// them as shared building blocks), so composing them does not widen access.
func authorizeComposition(
	ctx context.Context, coord *authz.Coordinator, subj authz.Subject, host string, spec RunSpec,
) error {
	if coord == nil || !coord.Enabled() {
		return nil
	}
	// host is empty for a hostless playground run. The empty name is what
	// selects the default authorization backend and makes only rules that apply
	// to every agent match; it is not a good label for an error message, so the
	// human-facing scope gets a readable name.
	scope := host
	if scope == "" {
		scope = "playground"
	}
	for _, ref := range []struct {
		kind  authz.ResourceKind
		names []string
	}{
		{authz.ResourceKindSkill, spec.Skills},
		{authz.ResourceKindMCP, spec.MCPs},
		{authz.ResourceKindKnowledgeBase, spec.KnowledgeBases},
	} {
		decision, reason, err := coord.AuthorizeResourceRefs(ctx, subj, host, ref.kind, ref.names)
		if err != nil {
			return fmt.Errorf("authz: %s authorization error for agent %q: %w", ref.kind, scope, err)
		}
		if decision != authz.DecisionDeny {
			continue
		}
		// The batch answer does not say which name denied. Re-check one at a
		// time so the refusal can name it; the batch is small and already
		// loaded, so the cost is one extra evaluation per name.
		denied := ref.names
		for _, name := range ref.names {
			one, oneReason, oneErr := coord.AuthorizeResourceRefs(ctx, subj, host, ref.kind, []string{name})
			if oneErr != nil {
				return fmt.Errorf("authz: %s authorization error for agent %q: %w", ref.kind, scope, oneErr)
			}
			if one == authz.DecisionDeny {
				denied = []string{name}
				if oneReason != "" {
					reason = oneReason
				}
				break
			}
		}
		return &authz.ErrAgentAccessDenied{
			Subject:   subj,
			AgentName: scope,
			Reason:    fmt.Sprintf("%s %s: %s", ref.kind, strings.Join(denied, ", "), reason),
		}
	}
	return nil
}
