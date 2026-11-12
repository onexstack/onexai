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

package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/labels"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	chatmodel "github.com/onexstack/onexai/internal/chat/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/dbschema"
)

// The kinds, by the name the API server uses for them. They key the placement
// map, so they have to match what the emitter is handed.
const (
	kindAgent             = "Agent"
	kindAgentBinding      = "AgentBinding"
	kindAuthorizationRule = "AuthorizationRule"
	kindDutyGroup         = "DutyGroup"
	kindKnowledgeBase     = "KnowledgeBase"
	kindMCPServer         = "MCPServer"
	kindModel             = "Model"
	kindPeerBinding       = "PeerBinding"
	kindPrompt            = "Prompt"
	kindSkill             = "Skill"
	kindWebhook           = "Webhook"
)

// placement answers "which namespace does this object live in?" for every
// object the migration is about to write.
//
// # Why a single --namespace cannot work
//
// In the declarative model the recorded namespace is the object's namespace:
// AgentM.Namespace round-trips through ObjectMeta.Namespace
// (controlplanestore.agentFromCR reads Namespace off cr.Namespace), and the
// runtime resolves references inside the referrer's own namespace — a Prompt or
// KnowledgeBase in another namespace is simply not found (see
// controlplanestore.convert.go's refName note and promptRefsFromCR, which keeps
// only same-namespace references). Writing everything into one namespace
// therefore either moves objects out of the namespace that gates them, or
// breaks the references of every agent that lives elsewhere.
//
// # The rule
//
//  1. The object's own namespace, when the row records one. That column is what
//     gates namespace-level authorization, so it wins.
//  2. Otherwise the namespace of the agents that reference it. Legacy rows
//     predate the column; the reference is the only statement of intent left,
//     and placing the object anywhere else would silently void it. Resources
//     nobody references fall back to DefaultNamespace, where they stay visible
//     and editable.
//  3. A row whose recorded namespace disagrees with its referrers, or whose
//     referrers disagree with each other, stops the migration. The object can
//     only live in one namespace; picking one silently would either widen a
//     namespace gate or break a reference, and neither is a decision a data
//     migration gets to make quietly.
type placement struct {
	fallback  string
	namespace map[string]map[string]string
	// origin says where each namespace came from, for the report: "workspace"
	// or the agent that pulled the object into its own.
	origin map[string]map[string]string
	// dangling lists references to objects the database has no row for. They
	// are not created here — the old runtime missed them too — but they are the
	// reason an agent can come up with fewer tools than its row names.
	dangling []string
}

// namespaceOf is the namespace the emitter should use for one object.
func (p *placement) namespaceOf(kind, name string) string {
	if byName, ok := p.namespace[kind]; ok {
		if ns, ok := byName[name]; ok {
			return ns
		}
	}
	return p.fallback
}

func (p *placement) originOf(kind, name string) string {
	if byName, ok := p.origin[kind]; ok {
		if why, ok := byName[name]; ok {
			return why
		}
	}
	return "fallback"
}

// reference is one edge in the graph: a named object of some kind, referenced
// from objects that live in the given namespaces.
type reference struct {
	namespaces map[string]struct{}
}

// planPlacement reads every table the migration touches and decides the
// namespace of every object in it.
func planPlacement(ctx context.Context, db *gorm.DB, fallback string) (*placement, error) {
	agents, err := findLegacyAgents(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read agents: %w", err)
	}
	skills, err := find[agentmodel.SkillM](ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read skills: %w", err)
	}
	knowledgeBases, err := find[agentmodel.KnowledgeBaseM](ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read knowledge bases: %w", err)
	}
	mcps, err := find[agentmodel.MCPM](ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read mcp servers: %w", err)
	}
	prompts, err := find[agentmodel.PromptM](ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read prompts: %w", err)
	}
	models, err := find[agentmodel.ModelM](ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read models: %w", err)
	}
	webhooks, err := find[agentmodel.WebhookM](ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read webhooks: %w", err)
	}
	dutyGroups, err := find[chatmodel.OncallGroupM](ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read duty groups: %w", err)
	}

	// The command grants are read through dbschema for the same reason the
	// migrator does: the model is an in-memory projection with no TableName.
	var bindings []chatmodel.AgentCommandM
	commandTable := dbschema.Table(dbschema.Chat, "agent_command")
	if err := db.WithContext(ctx).Table(commandTable).Find(&bindings).Error; err != nil {
		return nil, fmt.Errorf("read %s: %w", commandTable, err)
	}

	p := &placement{
		fallback:  fallback,
		namespace: map[string]map[string]string{},
		origin:    map[string]map[string]string{},
	}

	// Agents place themselves: the recorded namespace is the object's
	// namespace, and an empty one is DefaultNamespace — the same normalization
	// every GetNamespace in the model package applies, so the gate keeps the
	// value it had.
	agentNamespaces := make(map[string]string, len(agents))
	recorded := map[string]map[string]string{kindAgent: {}, kindSkill: {}, kindKnowledgeBase: {}, kindMCPServer: {}}
	for i := range agents {
		row := &agents[i]
		ns := agentmodel.NormalizeNamespace(row.Namespace)
		agentNamespaces[row.Name] = ns
		recorded[kindAgent][row.Name] = ns
	}
	for i := range skills {
		recorded[kindSkill][skills[i].Name] = skills[i].Namespace
	}
	for i := range knowledgeBases {
		recorded[kindKnowledgeBase][knowledgeBases[i].Name] = knowledgeBases[i].Namespace
	}
	for i := range mcps {
		recorded[kindMCPServer][mcps[i].Name] = mcps[i].Namespace
	}

	// Everything else has no workspace column: they were global tables, and the
	// only thing that says where a row belongs is who uses it.
	refs := map[string]map[string]*reference{
		kindPrompt:        {},
		kindModel:         {},
		kindWebhook:       {},
		kindSkill:         {},
		kindKnowledgeBase: {},
		kindMCPServer:     {},
		kindAgent:         {},
	}
	addRef := func(kind, name, namespace string) {
		name = strings.TrimSpace(name)
		if name == "" || name == "*" {
			return
		}
		byName, ok := refs[kind]
		if !ok {
			byName = map[string]*reference{}
			refs[kind] = byName
		}
		ref, ok := byName[name]
		if !ok {
			ref = &reference{namespaces: map[string]struct{}{}}
			byName[name] = ref
		}
		ref.namespaces[namespace] = struct{}{}
	}

	// The agent rows are the only place references are written down.
	for i := range agents {
		row := &agents[i]
		ns := agentNamespaces[row.Name]
		if !isInlinePromptValue(row.SysPrompt) && row.SysPrompt != "" && row.SysPrompt != agentv1alpha1.BuiltinPrompt {
			addRef(kindPrompt, row.SysPrompt, ns)
		}
		addRef(kindModel, row.Model, ns)
		for _, name := range row.Skills {
			addRef(kindSkill, name, ns)
		}
		for _, name := range row.MCPs {
			addRef(kindMCPServer, name, ns)
		}
		for _, name := range row.KnowledgeBases {
			addRef(kindKnowledgeBase, name, ns)
		}
		for _, name := range row.Webhooks {
			addRef(kindWebhook, name, ns)
		}
	}

	// A binding names agents, so it lives where they do: the runtime matches a
	// binding against agents in its own namespace.
	agentLabels, err := legacyLabels(ctx, db, &agentmodel.AgentM{})
	if err != nil {
		return nil, fmt.Errorf("read agent labels: %w", err)
	}
	bindingNames := make([]string, 0, len(bindings))
	for i := range bindings {
		bindingNames = append(bindingNames, bindings[i].Name)
	}
	sort.Strings(bindingNames)
	bindingNamespaces := make(map[string]string, len(bindings))
	for i := range bindings {
		row := &bindings[i]
		spaces := map[string]struct{}{}
		for _, agent := range selectingAgents(row, agentNamespaces, agentLabels) {
			spaces[agentNamespaces[agent]] = struct{}{}
		}
		switch len(spaces) {
		case 0:
			bindingNamespaces[row.Name] = fallback
		case 1:
			for ns := range spaces {
				bindingNamespaces[row.Name] = ns
			}
		default:
			return nil, fmt.Errorf(
				"AgentBinding %q selects agents in %s; a binding lives in one namespace, so which workspace owns it is a decision for a person",
				row.Name, strings.Join(sortedKeys(spaces), ", "))
		}
	}

	// Now place every object.
	place := func(kind, name, recordedWorkspace string, hasWorkspaceColumn bool) error {
		if hasWorkspaceColumn && recordedWorkspace != "" {
			p.namespace[kind][name] = recordedWorkspace
			p.origin[kind][name] = "workspace"
		}
		ref, ok := refs[kind][name]
		if !ok || len(ref.namespaces) == 0 {
			if _, done := p.namespace[kind][name]; !done {
				p.namespace[kind][name] = fallback
				p.origin[kind][name] = "unreferenced"
			}
			return nil
		}
		spaces := sortedKeys(ref.namespaces)
		if hasWorkspaceColumn && recordedWorkspace != "" {
			for _, ns := range spaces {
				if ns != recordedWorkspace {
					return fmt.Errorf(
						"%s %q records workspace %q but is referenced from %s; a reference only resolves inside its own namespace, so one of the two is wrong",
						kind, name, recordedWorkspace, ns)
				}
			}
			return nil
		}
		if len(spaces) > 1 {
			return fmt.Errorf(
				"%s %q is referenced from %s; it can only live in one namespace, and splitting it would break a reference",
				kind, name, strings.Join(spaces, ", "))
		}
		p.namespace[kind][name] = spaces[0]
		p.origin[kind][name] = "referenced"
		return nil
	}

	// Recorded workspaces first: they are the authoritative statement, and the
	// reference check below compares against them.
	ensure := func(kind string) {
		if p.namespace[kind] == nil {
			p.namespace[kind] = map[string]string{}
		}
		if p.origin[kind] == nil {
			p.origin[kind] = map[string]string{}
		}
	}
	for kind := range recorded {
		ensure(kind)
	}
	for kind := range refs {
		ensure(kind)
	}
	ensure(kindWebhook)
	ensure(kindDutyGroup)
	ensure(kindAgentBinding)
	ensure(kindAuthorizationRule)

	for i := range agents {
		if err := place(kindAgent, agents[i].Name, agentNamespaces[agents[i].Name], true); err != nil {
			return nil, err
		}
	}
	for i := range skills {
		if err := place(kindSkill, skills[i].Name, skills[i].Namespace, true); err != nil {
			return nil, err
		}
	}
	for i := range knowledgeBases {
		if err := place(kindKnowledgeBase, knowledgeBases[i].Name, knowledgeBases[i].Namespace, true); err != nil {
			return nil, err
		}
	}
	for i := range mcps {
		if err := place(kindMCPServer, mcps[i].Name, mcps[i].Namespace, true); err != nil {
			return nil, err
		}
	}
	for i := range prompts {
		if err := place(kindPrompt, prompts[i].Name, "", false); err != nil {
			return nil, err
		}
	}
	for i := range models {
		if err := place(kindModel, models[i].Name, "", false); err != nil {
			return nil, err
		}
	}
	for i := range webhooks {
		if err := place(kindWebhook, webhooks[i].Name, "", false); err != nil {
			return nil, err
		}
	}
	for i := range dutyGroups {
		if err := place(kindDutyGroup, dutyGroups[i].Name, dutyGroups[i].Namespace, true); err != nil {
			return nil, err
		}
	}
	for _, name := range bindingNames {
		p.namespace[kindAgentBinding][name] = bindingNamespaces[name]
		p.origin[kindAgentBinding][name] = "agents it selects"
	}

	// Inline prompts are derived per agent, so they live wherever that agent does.
	for i := range agents {
		row := &agents[i]
		if !isInlinePromptValue(row.SysPrompt) {
			continue
		}
		name := agentv1alpha1.InlinePromptName(row.Name)
		ensure(kindPrompt)
		p.namespace[kindPrompt][name] = agentNamespaces[row.Name]
		p.origin[kindPrompt][name] = "inline for " + row.Name
	}

	p.dangling = danglingReferences(refs, recorded, prompts, models, webhooks)
	return p, nil
}

// danglingReferences lists references whose target the database has no row
// for. They are reported, not fatal: the old runtime did not resolve them
// either, so refusing would hold the migration hostage to data it is faithfully
// carrying over.
func danglingReferences(
	refs map[string]map[string]*reference,
	recorded map[string]map[string]string,
	prompts []agentmodel.PromptM,
	models []agentmodel.ModelM,
	webhooks []agentmodel.WebhookM,
) []string {
	exists := map[string]map[string]struct{}{}
	for kind, byName := range recorded {
		exists[kind] = map[string]struct{}{}
		for name := range byName {
			exists[kind][name] = struct{}{}
		}
	}
	for _, rows := range []struct {
		kind  string
		names func() []string
	}{
		{kindPrompt, func() []string { return namesOf(prompts, func(i *agentmodel.PromptM) string { return i.Name }) }},
		{kindModel, func() []string { return namesOf(models, func(i *agentmodel.ModelM) string { return i.Name }) }},
		{kindWebhook, func() []string { return namesOf(webhooks, func(i *agentmodel.WebhookM) string { return i.Name }) }},
	} {
		if exists[rows.kind] == nil {
			exists[rows.kind] = map[string]struct{}{}
		}
		for _, name := range rows.names() {
			exists[rows.kind][name] = struct{}{}
		}
	}

	var out []string
	for kind, byName := range refs {
		for name, ref := range byName {
			if _, ok := exists[kind][name]; ok {
				continue
			}
			out = append(out, fmt.Sprintf("%s %q (from %s)", kind, name, strings.Join(sortedKeys(ref.namespaces), ", ")))
		}
	}
	sort.Strings(out)
	return out
}

// report prints the namespace of every object, and why. The migration writes
// files a person is expected to read before creating them; the placement is the
// part that is not visible in the manifests themselves.
func (p *placement) report() {
	kinds := make([]string, 0, len(p.namespace))
	for kind := range p.namespace {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	fmt.Fprintf(stderr, "\nplacement (namespace ← why)\n")
	for _, kind := range kinds {
		names := make([]string, 0, len(p.namespace[kind]))
		for name := range p.namespace[kind] {
			names = append(names, name)
		}
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(stderr, "  %-16s %-40s %-14s %s\n", kind, name, p.namespace[kind][name], p.originOf(kind, name))
		}
	}
	if len(p.dangling) > 0 {
		fmt.Fprintf(stderr, "\nreferenced but absent from the database (not created, exactly as the SQL-backed runtime saw them):\n")
		for _, item := range p.dangling {
			fmt.Fprintf(stderr, "  %s\n", item)
		}
	}
	fmt.Fprintln(stderr)
}

// selectingAgents returns the agents a binding selects, through whichever of
// the three ways the row expresses it — the explicit list, the metadata.name
// field selector, or the label selector — restricted to agents that exist.
//
// All three matter: the legacy table used them interchangeably, so a binding
// whose agents column is empty but whose field selector names an agent is still
// a binding for that agent.
func selectingAgents(row *chatmodel.AgentCommandM, namespaces map[string]string, agentLabels map[string]agentmodel.StringMap) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 4)
	add := func(name string) {
		if _, ok := namespaces[name]; !ok {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}

	for _, name := range row.AgentList() {
		add(name)
	}
	if row.AgentFieldSelector != "" {
		// Only metadata.name is a list of names; a field selector on
		// metadata.workspace names a workspace, so a parse failure here is not
		// an error, just one avenue that cannot say which agents are selected.
		if names, err := agentNamesFromFieldSelector(row.AgentFieldSelector); err == nil {
			for _, name := range names {
				add(name)
			}
		}
	}
	if row.AgentSelector != "" {
		if selector, err := labels.Parse(row.AgentSelector); err == nil {
			for name := range namespaces {
				if selector.Matches(labels.Set(agentLabels[name])) {
					add(name)
				}
			}
		}
	}

	sort.Strings(out)
	return out
}

// namespaces returns every namespace the migration writes into, sorted.
//
// They are workspaces, and a workspace is a namespace the control plane does
// not create for you: it only brings up its own system namespaces. Creating
// them is therefore part of the migration, not a detail of `kubectl create -f`.
func (p *placement) namespaces() []string {
	seen := map[string]struct{}{}
	for _, byName := range p.namespace {
		for _, ns := range byName {
			seen[ns] = struct{}{}
		}
	}
	if len(seen) == 0 {
		seen[p.fallback] = struct{}{}
	}
	return sortedKeys(seen)
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
