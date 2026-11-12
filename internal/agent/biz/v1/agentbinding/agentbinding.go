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

// Package agentbinding implements the AgentBinding business logic: the CRUD
// surface, the effective view of an agent, and the dry-run preview.
package agentbinding

import (
	"context"
	"errors"
	"sort"

	"github.com/onexstack/onexstack/pkg/store/where"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// AgentBindingBiz is the AgentBinding business logic.
type AgentBindingBiz interface {
	// Create stores a new binding.
	Create(ctx context.Context, req *v1.CreateAgentBindingRequest) (*v1.CreateAgentBindingResponse, error)
	// Get returns one binding by name.
	Get(ctx context.Context, req *v1.GetAgentBindingRequest) (*v1.GetAgentBindingResponse, error)
	// List returns a page of bindings.
	List(ctx context.Context, req *v1.ListAgentBindingRequest) (*v1.ListAgentBindingResponse, error)
	// Update rewrites one binding.
	Update(ctx context.Context, req *v1.UpdateAgentBindingRequest) (*v1.UpdateAgentBindingResponse, error)
	// Delete removes one binding by name.
	Delete(ctx context.Context, req *v1.DeleteAgentBindingRequest) (*v1.DeleteAgentBindingResponse, error)
	// GetEffective answers what an agent actually has after the controller has
	// resolved its bindings.
	GetEffective(ctx context.Context, req *v1.GetAgentRequest) (*v1.GetEffectiveAgentResponse, error)
	// Preview is the dry run for a binding that does not exist yet, or for an
	// edit to one.
	Preview(ctx context.Context, req *v1.PreviewAgentBindingRequest) (*v1.PreviewAgentBindingResponse, error)
}

// agentBindingBiz implements AgentBindingBiz.
type agentBindingBiz struct {
	store       agentstore.AIStore
	invalidator mcpclient.Invalidator
}

var _ AgentBindingBiz = (*agentBindingBiz)(nil)

// New creates an AgentBindingBiz. invalidator may be nil when the deployment
// has no MCP runtime.
func New(store agentstore.AIStore, invalidator mcpclient.Invalidator) *agentBindingBiz {
	return &agentBindingBiz{store: store, invalidator: invalidator}
}

// invalidateTools drops the discovered MCP tool sets when a binding write can
// change what an agent sees. The interface and the manager are both nil-safe.
func (b *agentBindingBiz) invalidateTools() {
	if b.invalidator != nil {
		b.invalidator.Invalidate()
	}
}

// Create implements AgentBindingBiz.
func (b *agentBindingBiz) Create(
	ctx context.Context, req *v1.CreateAgentBindingRequest,
) (*v1.CreateAgentBindingResponse, error) {
	created, err := b.store.AgentBindings().Create(ctx, conversion.AgentBindingV1ToCR(req.GetAgentBinding()))
	if err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.CreateAgentBindingResponse{
		AgentBinding: conversion.AgentBindingCRToV1(created),
	}, nil
}

// Get implements AgentBindingBiz.
func (b *agentBindingBiz) Get(
	ctx context.Context, req *v1.GetAgentBindingRequest,
) (*v1.GetAgentBindingResponse, error) {
	item, err := b.store.AgentBindings().Get(ctx, req.GetName())
	if err := storeerr.GetError(item, err, "agent binding %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetAgentBindingResponse{
		AgentBinding: conversion.AgentBindingCRToV1(item),
	}, nil
}

// List implements AgentBindingBiz.
func (b *agentBindingBiz) List(
	ctx context.Context, req *v1.ListAgentBindingRequest,
) (*v1.ListAgentBindingResponse, error) {
	total, list, err := b.store.AgentBindings().List(ctx, int(req.GetOffset()), int(req.GetLimit()))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	bindings := make([]*v1.AgentBinding, 0, len(list))
	for _, item := range list {
		bindings = append(bindings, conversion.AgentBindingCRToV1(item))
	}
	return &v1.ListAgentBindingResponse{AgentBindings: bindings, Count: total}, nil
}

// Update implements AgentBindingBiz.
func (b *agentBindingBiz) Update(
	ctx context.Context, req *v1.UpdateAgentBindingRequest,
) (*v1.UpdateAgentBindingResponse, error) {
	updated, err := b.store.AgentBindings().Update(ctx, conversion.AgentBindingV1ToCR(req.GetAgentBinding()))
	if err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.UpdateAgentBindingResponse{
		AgentBinding: conversion.AgentBindingCRToV1(updated),
	}, nil
}

// Delete implements AgentBindingBiz.
func (b *agentBindingBiz) Delete(
	ctx context.Context, req *v1.DeleteAgentBindingRequest,
) (*v1.DeleteAgentBindingResponse, error) {
	if err := b.store.AgentBindings().Delete(ctx, req.GetName()); err != nil {
		return nil, storeerr.Write(err)
	}
	b.invalidateTools()
	return &v1.DeleteAgentBindingResponse{}, nil
}

// GetEffective implements AgentBindingBiz.
func (b *agentBindingBiz) GetEffective(
	ctx context.Context, req *v1.GetAgentRequest,
) (*v1.GetEffectiveAgentResponse, error) {
	name := req.GetName()
	a, err := b.store.Agents().Get(ctx, where.F("name", name))
	if err := storeerr.GetError(a, err, "agent %q not found", name); err != nil {
		return nil, err
	}

	return &v1.GetEffectiveAgentResponse{
		Agent:        a.Name,
		Observed:     a.Observed,
		ResolvedHash: a.ResolvedHash,
		Capabilities: v1.EffectiveAgentCapabilities{
			Model:           a.EffectiveModel(),
			Skills:          []string(a.EffectiveSkills()),
			MCPs:            []string(a.EffectiveMCPs()),
			KnowledgeBases:  []string(a.EffectiveKnowledgeBases()),
			CustomCommands:  []string(a.EffectiveCustomCommands()),
			Commands:        []string(a.EffectiveCommands()),
			RequireApproval: []string(a.EffectiveRequireApproval()),
			Webhooks:        []string(a.EffectiveWebhooks()),
			Templates:       []string(a.EffectiveTemplates()),
			Delegates:       []string(a.EffectiveDelegates()),
			Calls:           []string(a.EffectiveCalls()),
			SessionTools:    a.EffectiveSessionTools(),
		},
		Bindings: v1.EffectiveAgentBindings{
			Total:     a.BindingContributionsTotal,
			Truncated: a.BindingContributionsTruncated,
			Items:     bindingContributions(a.BindingContributions),
		},
		Unresolved: []string(a.Missing),
	}, nil
}

// bindingContributions renders the contributions a controller recorded on the
// agent. A nil slice stays nil so the response keeps omitting the field the way
// the model's JSON tags do.
func bindingContributions(in []model.BindingContributionM) []v1.EffectiveAgentContribution {
	if in == nil {
		return nil
	}
	out := make([]v1.EffectiveAgentContribution, 0, len(in))
	for _, contribution := range in {
		out = append(out, v1.EffectiveAgentContribution{
			Name:               contribution.Name,
			Generation:         contribution.Generation,
			Hash:               contribution.Hash,
			PromptCount:        contribution.PromptCount,
			SkillCount:         contribution.SkillCount,
			MCPCount:           contribution.MCPCount,
			KnowledgeBaseCount: contribution.KnowledgeBaseCount,
			CustomCommandCount: contribution.CustomCommandCount,
			CommandCount:       contribution.CommandCount,
		})
	}
	return out
}

// Preview implements AgentBindingBiz.
//
// It resolves the same two questions the controllers resolve — who matches, and
// what the references resolve to — against the live caches. Nothing is written.
func (b *agentBindingBiz) Preview(
	ctx context.Context, req *v1.PreviewAgentBindingRequest,
) (*v1.PreviewAgentBindingResponse, error) {
	matched, err := b.previewMatchedAgents(ctx, req.Spec.Selector, req.Spec.Agents, req.Namespace)
	if err != nil {
		return nil, err
	}

	resolved, missing, err := b.previewContribution(ctx, req.Spec, req.Namespace)
	if err != nil {
		return nil, err
	}

	return &v1.PreviewAgentBindingResponse{
		MatchedAgents: matched,
		Resolved:      resolved,
		Missing:       emptyIfNil(missing),
	}, nil
}

func (b *agentBindingBiz) previewMatchedAgents(
	ctx context.Context, selector *metav1.LabelSelector, names []string, namespace string,
) ([]string, error) {
	selectorObj, err := labelSelector(selector)
	if err != nil {
		return nil, errno.ErrBind.WithMessage("%s", err.Error())
	}

	_, agents, err := b.store.Agents().List(ctx, where.L(-1))
	if err != nil {
		return nil, storeerr.Read(err)
	}

	named := make(map[string]struct{}, len(names))
	for _, name := range names {
		named[name] = struct{}{}
	}

	matched := make([]string, 0, len(agents))
	for _, a := range agents {
		if !a.Enabled() {
			continue
		}
		if namespace != "" && a.Namespace != namespace {
			continue
		}
		_, byName := named[a.Name]
		if byName || (selectorObj != nil && selectorObj.Matches(labels.Set(a.Labels))) {
			matched = append(matched, a.Name)
		}
	}
	sort.Strings(matched)
	return matched, nil
}

func (b *agentBindingBiz) previewContribution(
	ctx context.Context, spec agentv1alpha1.AgentBindingSpec, namespace string,
) (v1.PreviewAgentBindingResolved, []string, error) {
	var missing []string

	// A reference is (namespace, name). The preview must answer the same object
	// a run would, so it resolves against the binding's namespace and keeps the
	// qualifier for a cross-namespace target.
	resolve := func(kind string, refs []agentv1alpha1.Reference, get func(namespace, name string) (bool, error)) ([]string, error) {
		out := make([]string, 0, len(refs))
		for _, ref := range refs {
			ns := ref.Namespace
			if ns == "" {
				ns = namespace
			}
			ok, err := get(ns, ref.Name)
			if err != nil {
				return nil, err
			}
			if !ok {
				missing = append(missing, kind+"/"+ns+"/"+ref.Name)
				continue
			}
			if ns != "" && ns != namespace {
				out = append(out, ns+"/"+ref.Name)
			} else {
				out = append(out, ref.Name)
			}
		}
		sort.Strings(out)
		return out, nil
	}

	prompts, err := resolve("Prompt", spec.Prompts, func(ns, name string) (bool, error) {
		entry := name
		if ns != "" {
			entry = ns + "/" + name
		}
		obj, err := b.store.Prompts().Get(ctx, model.EntryFilter(entry))
		if errors.Is(err, agentstore.ErrNotFound) {
			return false, nil
		}
		return obj != nil && obj.Enabled(), storeerr.Read(err)
	})
	if err != nil {
		return v1.PreviewAgentBindingResolved{}, nil, err
	}

	skills, err := resolve("Skill", spec.Skills, func(ns, name string) (bool, error) {
		entry := name
		if ns != "" {
			entry = ns + "/" + name
		}
		obj, err := b.store.Skills().Get(ctx, model.EntryFilter(entry))
		if errors.Is(err, agentstore.ErrNotFound) {
			return false, nil
		}
		return obj != nil && obj.Enabled(), storeerr.Read(err)
	})
	if err != nil {
		return v1.PreviewAgentBindingResolved{}, nil, err
	}

	knowledgeBases, err := resolve("KnowledgeBase", spec.KnowledgeBases, func(ns, name string) (bool, error) {
		entry := name
		if ns != "" {
			entry = ns + "/" + name
		}
		obj, err := b.store.KnowledgeBases().Get(ctx, model.EntryFilter(entry))
		if errors.Is(err, agentstore.ErrNotFound) {
			return false, nil
		}
		return obj != nil && obj.Enabled(), storeerr.Read(err)
	})
	if err != nil {
		return v1.PreviewAgentBindingResolved{}, nil, err
	}

	mcps, err := resolve("MCPServer", spec.MCPServers, func(ns, name string) (bool, error) {
		entry := name
		if ns != "" {
			entry = ns + "/" + name
		}
		obj, err := b.store.MCPs().Get(ctx, model.EntryFilter(entry))
		if errors.Is(err, agentstore.ErrNotFound) {
			return false, nil
		}
		return obj != nil && obj.Enabled(), storeerr.Read(err)
	})
	if err != nil {
		return v1.PreviewAgentBindingResolved{}, nil, err
	}

	customCommands, err := resolve("CustomCommand", spec.CustomCommands, func(ns, name string) (bool, error) {
		entry := name
		if ns != "" {
			entry = ns + "/" + name
		}
		obj, err := b.store.CustomCommands().Get(ctx, model.EntryFilter(entry))
		if errors.Is(err, agentstore.ErrNotFound) {
			return false, nil
		}
		return obj != nil && obj.Enabled(), storeerr.Read(err)
	})
	if err != nil {
		return v1.PreviewAgentBindingResolved{}, nil, err
	}

	commands, err := b.previewCommands(ctx, spec)
	if err != nil {
		return v1.PreviewAgentBindingResolved{}, nil, err
	}

	return v1.PreviewAgentBindingResolved{
		Prompts:        emptyIfNil(prompts),
		Skills:         emptyIfNil(skills),
		MCPs:           emptyIfNil(mcps),
		KnowledgeBases: emptyIfNil(knowledgeBases),
		CustomCommands: emptyIfNil(customCommands),
		Commands:       emptyIfNil(commands),
	}, missing, nil
}

// previewCommands merges the named commands with the ones the selector matches.
//
// The catalogue is optional. A store that does not serve it reports the named
// commands only — a smaller answer, never a wrong grant — because the selector
// itself is still what the controller and the runtime will expand.
func (b *agentBindingBiz) previewCommands(
	ctx context.Context, spec agentv1alpha1.AgentBindingSpec,
) ([]string, error) {
	granted := make(map[string]struct{}, len(spec.Commands))
	for _, name := range spec.Commands {
		granted[name] = struct{}{}
	}

	if selectorSpec := spec.CommandSelector; selectorSpec != nil {
		selector, err := metav1.LabelSelectorAsSelector(selectorSpec)
		switch {
		case err != nil:
			// Validation refuses this on write; a preview grants nothing by label.
		case selector.Empty():
			// Refused on write; Everything() would report the whole catalogue.
		default:
			if catalog, ok := b.store.(agentstore.CommandCatalog); ok {
				items, listErr := catalog.ListCommands(ctx)
				if listErr != nil {
					return nil, storeerr.Read(listErr)
				}
				for _, item := range items {
					if !item.Spec.Enabled {
						continue
					}
					if selector.Matches(labels.Set(item.Labels)) {
						granted[item.Name] = struct{}{}
					}
				}
			}
		}
	}

	out := make([]string, 0, len(granted))
	for name := range granted {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func labelSelector(selector *metav1.LabelSelector) (labels.Selector, error) {
	if selector == nil {
		return nil, nil
	}
	return metav1.LabelSelectorAsSelector(selector)
}

func emptyIfNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
