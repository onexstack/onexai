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

// Package skill implements the Skill business logic.
package skill

import (
	"context"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// ContextSkill is one skill an agent has exposed as a command.
//
// It is deliberately not model.SkillM: that is a repository record, and one
// record can hold several skills. What a person types after the slash is the
// skill's own name, which lives inside the repository.
type ContextSkill struct {
	// Name is what is typed after the slash.
	Name string
	// Description is what a menu shows, falling back to the repository's own
	// summary when the skill carries none.
	Description string
	// Repo is the skill record the skill was found in.
	Repo string
	// Provider is that record's kind (github, gitlab, lark_doc).
	Provider string
}

// ContextSkillSource enumerates the skills an agent exposes as commands.
//
// It is an interface so the business logic can be tested without a skill
// subsystem, and so the fact that enumerating a skill means reading a
// repository stays out of the HTTP layer.
type ContextSkillSource interface {
	// ContextSkills returns the skills agentName has exposed as commands, or an
	// error when the agent is unknown.
	ContextSkills(ctx context.Context, agentName string) ([]ContextSkill, error)
}

// SkillBiz is the Skill business logic.
type SkillBiz interface {
	Create(ctx context.Context, req *v1.CreateSkillRequest) (*v1.CreateSkillResponse, error)
	Get(ctx context.Context, req *v1.GetSkillRequest) (*v1.GetSkillResponse, error)
	List(ctx context.Context, req *v1.ListSkillRequest) (*v1.ListSkillResponse, error)
	Update(ctx context.Context, req *v1.UpdateSkillRequest) (*v1.UpdateSkillResponse, error)
	Delete(ctx context.Context, req *v1.DeleteSkillRequest) (*v1.DeleteSkillResponse, error)
	// ListContextSkill answers the skills an agent exposes as commands. It
	// exists because a skill's name is not in the record the CRUD face serves:
	// the record names a repository, and the skills inside it are only knowable
	// by having the skill subsystem list them.
	ListContextSkill(ctx context.Context, req *v1.GetAgentRequest) (*v1.ListContextSkillResponse, error)
}

type skillBiz struct {
	store  agentstore.AIStore
	source ContextSkillSource
}

var _ SkillBiz = (*skillBiz)(nil)

// New creates a SkillBiz.
func New(store agentstore.AIStore, source ContextSkillSource) *skillBiz {
	return &skillBiz{store: store, source: source}
}

// Create implements SkillBiz.
func (b *skillBiz) Create(ctx context.Context, req *v1.CreateSkillRequest) (*v1.CreateSkillResponse, error) {
	row := conversion.SkillV1ToSkillModel(req.GetSkill())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.Skills().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.CreateSkillResponse{Skill: conversion.SkillModelToSkillV1(row)}, nil
}

// Get implements SkillBiz.
func (b *skillBiz) Get(ctx context.Context, req *v1.GetSkillRequest) (*v1.GetSkillResponse, error) {
	row, err := b.store.Skills().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "skill %q not found", req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetSkillResponse{Skill: conversion.SkillModelToSkillV1(row)}, nil
}

// List implements SkillBiz.
func (b *skillBiz) List(ctx context.Context, req *v1.ListSkillRequest) (*v1.ListSkillResponse, error) {
	total, list, err := b.store.Skills().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if authzview.ReadFiltered(ctx, "skill") {
		before := len(list)
		list = authzview.Filter(ctx, "skill", list, func(row *model.SkillM) (string, string, string) {
			return row.Namespace, row.Name, ""
		})
		total = authzview.AdjustCount(total, before-len(list))
	}
	skills := make([]*v1.Skill, 0, len(list))
	for _, row := range list {
		skills = append(skills, conversion.SkillModelToSkillV1(row))
	}
	return &v1.ListSkillResponse{Skills: skills, Count: total}, nil
}

// Update implements SkillBiz.
//
// A guide/tool read withholds Params, so a client that PUTs back what it fetched
// would otherwise erase the repository it cannot see. The stored row fills the
// values the request omitted, including the three new fields a pre-existing
// client does not send. The merge lives here rather than in the validator
// because it is a write concern: validation checks the request as sent.
func (b *skillBiz) Update(ctx context.Context, req *v1.UpdateSkillRequest) (*v1.UpdateSkillResponse, error) {
	row := conversion.SkillV1ToSkillModel(req.GetSkill())
	existing, err := b.store.Skills().Get(ctx, where.F("name", row.Name))
	if err := storeerr.GetError(existing, err, "skill %q not found", row.Name); err != nil {
		return nil, err
	}

	mergeSkillUpdate(row, existing)
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.Skills().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.UpdateSkillResponse{Skill: conversion.SkillModelToSkillV1(row)}, nil
}

// mergeSkillUpdate fills the fields a client cannot see on a read.
func mergeSkillUpdate(row, existing *model.SkillM) {
	if row.Mode == "" {
		row.Mode = existing.Mode
	}
	if row.Params == "" {
		row.Params = existing.Params
	}
	if row.Contract == "" {
		row.Contract = existing.Contract
	}
	if row.Entrypoint == "" {
		row.Entrypoint = existing.Entrypoint
	}
	if row.CreatedAt == 0 {
		row.CreatedAt = existing.CreatedAt
	}
}

// Delete implements SkillBiz.
func (b *skillBiz) Delete(ctx context.Context, req *v1.DeleteSkillRequest) (*v1.DeleteSkillResponse, error) {
	if err := b.store.Skills().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	return &v1.DeleteSkillResponse{}, nil
}

// ListContextSkill implements SkillBiz.
func (b *skillBiz) ListContextSkill(
	ctx context.Context, req *v1.GetAgentRequest,
) (*v1.ListContextSkillResponse, error) {
	if b.source == nil {
		// Not an empty list: an empty list says the agent has no skills, which is
		// a different statement from "this deployment has no skill subsystem to
		// ask".
		return nil, errno.ErrNotFound.WithMessage("skills are not configured on this deployment")
	}

	found, err := b.source.ContextSkills(ctx, req.GetName())
	if err != nil {
		return nil, errno.ErrInternal.WithMessage("failed to list the agent's skills")
	}

	resp := &v1.ListContextSkillResponse{Skills: make([]*v1.ContextSkill, 0, len(found))}
	for _, s := range found {
		resp.Skills = append(resp.Skills, &v1.ContextSkill{
			Name:        s.Name,
			Description: s.Description,
			Repo:        s.Repo,
			Provider:    s.Provider,
		})
	}
	return resp, nil
}
