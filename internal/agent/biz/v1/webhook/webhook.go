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

// Package webhook implements the run-lifecycle hook business logic.
package webhook

import (
	"context"
	"errors"

	"github.com/onexstack/onexstack/pkg/errorsx"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	"github.com/onexstack/onexai/internal/agent/pkg/webhook"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// WebhookStore is the hook administration surface this business logic needs. It
// is a port rather than the concrete *webhook.Service so a test can inject a
// fake.
type WebhookStore interface {
	Create(ctx context.Context, hook *model.WebhookM) error
	Get(ctx context.Context, name string) (*model.WebhookM, bool, error)
	List(ctx context.Context) ([]*model.WebhookM, error)
	Update(ctx context.Context, hook *model.WebhookM) error
	Delete(ctx context.Context, name string) error
}

// errAPIUnavailable is returned when the surface was registered but no hook
// store was wired, so there is no hook table to administer. It is an explicit
// refusal rather than a 404 so an operator can tell "wrong URL" from "this
// deployment cannot store hooks".
var errAPIUnavailable = errno.ErrInternal.WithMessage(
	"webhook API is unavailable: no hook store is wired")

// WebhookBiz is the run-lifecycle hook business logic.
type WebhookBiz interface {
	Create(ctx context.Context, req *v1.CreateWebhookRequest) (*v1.CreateWebhookResponse, error)
	Get(ctx context.Context, req *v1.GetWebhookRequest) (*v1.GetWebhookResponse, error)
	List(ctx context.Context, req *v1.ListWebhookRequest) (*v1.ListWebhookResponse, error)
	Update(ctx context.Context, req *v1.UpdateWebhookRequest) (*v1.UpdateWebhookResponse, error)
	Delete(ctx context.Context, req *v1.DeleteWebhookRequest) (*v1.DeleteWebhookResponse, error)
}

type webhookBiz struct{ store WebhookStore }

var _ WebhookBiz = (*webhookBiz)(nil)

// New creates a WebhookBiz. A nil store makes every method refuse with the
// unavailability error.
func New(store WebhookStore) *webhookBiz { return &webhookBiz{store: store} }

// Create implements WebhookBiz.
func (b *webhookBiz) Create(
	ctx context.Context, req *v1.CreateWebhookRequest,
) (*v1.CreateWebhookResponse, error) {
	if b.store == nil {
		return nil, errAPIUnavailable
	}
	hook := conversion.WebhookV1ToWebhookModel(req.GetWebhook())
	if err := b.store.Create(ctx, hook); err != nil {
		return nil, webhookError(err, errno.ErrDBWrite)
	}
	return &v1.CreateWebhookResponse{Webhook: conversion.WebhookModelToWebhookV1(hook)}, nil
}

// Get implements WebhookBiz.
func (b *webhookBiz) Get(ctx context.Context, req *v1.GetWebhookRequest) (*v1.GetWebhookResponse, error) {
	if b.store == nil {
		return nil, errAPIUnavailable
	}
	hook, found, err := b.store.Get(ctx, req.GetName())
	if err != nil {
		return nil, webhookError(err, errno.ErrDBRead)
	}
	if !found {
		return nil, errno.ErrNotFound.WithMessage("webhook %q not found", req.GetName())
	}
	return &v1.GetWebhookResponse{Webhook: conversion.WebhookModelToWebhookV1(hook)}, nil
}

// List implements WebhookBiz.
func (b *webhookBiz) List(ctx context.Context, req *v1.ListWebhookRequest) (*v1.ListWebhookResponse, error) {
	if b.store == nil {
		return nil, errAPIUnavailable
	}
	list, err := b.store.List(ctx)
	if err != nil {
		return nil, webhookError(err, errno.ErrDBRead)
	}
	// The hook store has no paged read of its own, and the whole set is already
	// in hand here, so the page is taken from the list rather than pushed down.
	// store.Page is the same helper the resource stores use, so the contract is
	// identical.
	// The store already returned the whole set, so filtering before paging
	// keeps both the page and the total honest.
	list = authzview.Filter(ctx, "webhook", list, func(row *model.WebhookM) (string, string, string) {
		return "", row.Name, row.Owner
	})
	total, page := agentstore.Page(list, int(req.GetOffset()), int(req.GetLimit()))
	hooks := make([]*v1.Webhook, 0, len(page))
	for _, w := range page {
		hooks = append(hooks, conversion.WebhookModelToWebhookV1(w))
	}
	return &v1.ListWebhookResponse{Webhooks: hooks, Count: total}, nil
}

// Update implements WebhookBiz.
func (b *webhookBiz) Update(
	ctx context.Context, req *v1.UpdateWebhookRequest,
) (*v1.UpdateWebhookResponse, error) {
	if b.store == nil {
		return nil, errAPIUnavailable
	}
	hook := conversion.WebhookV1ToWebhookModel(req.GetWebhook())
	if err := b.store.Update(ctx, hook); err != nil {
		return nil, webhookError(err, errno.ErrDBWrite)
	}
	return &v1.UpdateWebhookResponse{Webhook: conversion.WebhookModelToWebhookV1(hook)}, nil
}

// Delete implements WebhookBiz.
func (b *webhookBiz) Delete(
	ctx context.Context, req *v1.DeleteWebhookRequest,
) (*v1.DeleteWebhookResponse, error) {
	if b.store == nil {
		return nil, errAPIUnavailable
	}
	if err := b.store.Delete(ctx, req.GetName()); err != nil {
		return nil, webhookError(err, errno.ErrDBWrite)
	}
	return &v1.DeleteWebhookResponse{}, nil
}

// webhookError maps the service's error taxonomy onto HTTP statuses: validation
// to 400, a missing hook to 404, everything else (the database, or a failed
// reload) to the caller-supplied 5xx.
func webhookError(err error, fallback *errorsx.ErrorX) error {
	var verr *webhook.ValidationError
	switch {
	case errors.As(err, &verr):
		return errno.ErrInvalidArgument.WithMessage("%s", verr.Error())
	case errors.Is(err, webhook.ErrNotFound):
		return errno.ErrNotFound.WithMessage("%s", err.Error())
	case errors.Is(err, webhook.ErrNotConfigured):
		return errAPIUnavailable
	default:
		return fallback.WithMessage("%s", err.Error())
	}
}
