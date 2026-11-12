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

// Package authzrule implements the authorization-rule business logic.
package authzrule

import (
	"context"
	"errors"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzrule"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// AuthzRuleStore is the rule administration surface this business logic needs.
// It is a port rather than the concrete *authzrule.Service so a test can inject
// a fake.
type AuthzRuleStore interface {
	Create(ctx context.Context, rule *model.AuthzRuleM) error
	Get(ctx context.Context, name string) (*model.AuthzRuleM, bool, error)
	List(ctx context.Context) ([]*model.AuthzRuleM, error)
	Update(ctx context.Context, rule *model.AuthzRuleM) error
	Delete(ctx context.Context, name string) error
}

// errAPIUnavailable is returned when the surface was registered but no rule
// store was wired, so there is no rule table to administer. It is an explicit
// refusal rather than a 404 so an operator can tell "wrong URL" from "this
// deployment cannot store rules".
var errAPIUnavailable = errno.ErrInternal.WithMessage(
	"authz rule API is unavailable: no rule store is wired")

// AuthzRuleBiz is the authorization-rule business logic.
type AuthzRuleBiz interface {
	Create(ctx context.Context, req *v1.CreateAuthzRuleRequest) (*v1.CreateAuthzRuleResponse, error)
	Get(ctx context.Context, req *v1.GetAuthzRuleRequest) (*v1.GetAuthzRuleResponse, error)
	List(ctx context.Context, req *v1.ListAuthzRuleRequest) (*v1.ListAuthzRuleResponse, error)
	Update(ctx context.Context, req *v1.UpdateAuthzRuleRequest) (*v1.UpdateAuthzRuleResponse, error)
	Delete(ctx context.Context, req *v1.DeleteAuthzRuleRequest) (*v1.DeleteAuthzRuleResponse, error)
}

type authzRuleBiz struct{ store AuthzRuleStore }

var _ AuthzRuleBiz = (*authzRuleBiz)(nil)

// New creates an AuthzRuleBiz. A nil store makes every method refuse with the
// unavailability error.
func New(store AuthzRuleStore) *authzRuleBiz { return &authzRuleBiz{store: store} }

// Create implements AuthzRuleBiz.
func (b *authzRuleBiz) Create(
	ctx context.Context, req *v1.CreateAuthzRuleRequest,
) (*v1.CreateAuthzRuleResponse, error) {
	if b.store == nil {
		return nil, errAPIUnavailable
	}
	rule := conversion.AuthzRuleV1ToAuthzRuleModel(req.GetAuthzRule())
	if err := b.store.Create(ctx, rule); err != nil {
		return nil, authzRuleError(err, errno.ErrDBWrite)
	}
	return &v1.CreateAuthzRuleResponse{AuthzRule: conversion.AuthzRuleModelToAuthzRuleV1(rule)}, nil
}

// Get implements AuthzRuleBiz.
func (b *authzRuleBiz) Get(
	ctx context.Context, req *v1.GetAuthzRuleRequest,
) (*v1.GetAuthzRuleResponse, error) {
	if b.store == nil {
		return nil, errAPIUnavailable
	}
	rule, found, err := b.store.Get(ctx, req.GetName())
	if err != nil {
		return nil, authzRuleError(err, errno.ErrDBRead)
	}
	if !found {
		return nil, errno.ErrNotFound.WithMessage("authz rule %q not found", req.GetName())
	}
	return &v1.GetAuthzRuleResponse{AuthzRule: conversion.AuthzRuleModelToAuthzRuleV1(rule)}, nil
}

// List implements AuthzRuleBiz.
func (b *authzRuleBiz) List(
	ctx context.Context, req *v1.ListAuthzRuleRequest,
) (*v1.ListAuthzRuleResponse, error) {
	if b.store == nil {
		return nil, errAPIUnavailable
	}
	list, err := b.store.List(ctx)
	if err != nil {
		return nil, authzRuleError(err, errno.ErrDBRead)
	}
	// The rule store has no paged read of its own, and the whole rule set is
	// already in hand here, so the page is taken from the list rather than
	// pushed down. Filtering happens before paging so the count describes the
	// filtered set, which is what the console's "policies naming this user"
	// view needs.
	filtered := make([]*model.AuthzRuleM, 0, len(list))
	for _, rule := range list {
		if ruleMatchesRequest(rule, req) {
			filtered = append(filtered, rule)
		}
	}
	total, page := agentstore.Page(filtered, int(req.GetOffset()), int(req.GetLimit()))
	rules := make([]*v1.AuthzRule, 0, len(page))
	for _, r := range page {
		rules = append(rules, conversion.AuthzRuleModelToAuthzRuleV1(r))
	}
	return &v1.ListAuthzRuleResponse{AuthzRules: rules, Count: total}, nil
}

// ruleMatchesRequest applies the console's policy filters. Every filter is an
// AND; an empty field is not a filter.
//
// The filters are deliberately matched against the plural shape and treat "*"
// as a match for any value: a policy that names every resource is one of the
// policies that names this one.
func ruleMatchesRequest(rule *model.AuthzRuleM, req *v1.ListAuthzRuleRequest) bool {
	if rule == nil {
		return false
	}
	if effect := strings.ToLower(strings.TrimSpace(req.GetEffect())); effect != "" {
		if strings.ToLower(rule.Effect) != effect {
			return false
		}
	}
	if enabled := strings.TrimSpace(req.GetEnabled()); enabled != "" {
		want := strings.EqualFold(enabled, "true")
		if rule.Enabled() != want {
			return false
		}
	}
	if subjectType := strings.ToLower(strings.TrimSpace(req.GetSubjectType())); subjectType != "" {
		matched := false
		for _, subject := range rule.EffectiveSubjects() {
			kind := strings.ToLower(strings.TrimSpace(subject.Kind))
			if kind == subjectType || kind == "*" {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if subjectID := strings.TrimSpace(req.GetSubjectId()); subjectID != "" {
		matched := false
		for _, subject := range rule.EffectiveSubjects() {
			name := strings.TrimSpace(subject.Name)
			if name == subjectID || name == "*" {
				matched = true
				break
			}
		}
		if !matched && rule.SubjectNamespace == subjectID {
			matched = true
		}
		if !matched {
			return false
		}
	}
	if kind := strings.TrimSpace(req.GetResourceKind()); kind != "" {
		matched := false
		for _, resource := range rule.EffectiveResources() {
			if resource.Kind == kind || resource.Kind == "*" {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if name := strings.TrimSpace(req.GetResourceName()); name != "" {
		matched := false
		for _, resource := range rule.EffectiveResources() {
			for _, candidate := range resource.Names {
				if candidate == name || candidate == "*" {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// Update implements AuthzRuleBiz.
func (b *authzRuleBiz) Update(
	ctx context.Context, req *v1.UpdateAuthzRuleRequest,
) (*v1.UpdateAuthzRuleResponse, error) {
	if b.store == nil {
		return nil, errAPIUnavailable
	}
	rule := conversion.AuthzRuleV1ToAuthzRuleModel(req.GetAuthzRule())
	if err := b.store.Update(ctx, rule); err != nil {
		return nil, authzRuleError(err, errno.ErrDBWrite)
	}
	return &v1.UpdateAuthzRuleResponse{AuthzRule: conversion.AuthzRuleModelToAuthzRuleV1(rule)}, nil
}

// Delete implements AuthzRuleBiz.
func (b *authzRuleBiz) Delete(
	ctx context.Context, req *v1.DeleteAuthzRuleRequest,
) (*v1.DeleteAuthzRuleResponse, error) {
	if b.store == nil {
		return nil, errAPIUnavailable
	}
	if err := b.store.Delete(ctx, req.GetName()); err != nil {
		return nil, authzRuleError(err, errno.ErrDBWrite)
	}
	return &v1.DeleteAuthzRuleResponse{}, nil
}

// authzRuleError maps the service's error taxonomy onto HTTP statuses.
func authzRuleError(err error, fallback *errorsx.ErrorX) error {
	var verr *authzrule.ValidationError
	switch {
	case errors.As(err, &verr):
		return errno.ErrInvalidArgument.WithMessage("%s", verr.Error())
	case errors.Is(err, authzrule.ErrNotFound):
		return errno.ErrNotFound.WithMessage("%s", err.Error())
	case errors.Is(err, authzrule.ErrNotConfigured):
		return errAPIUnavailable
	default:
		return fallback.WithMessage("%s", err.Error())
	}
}
