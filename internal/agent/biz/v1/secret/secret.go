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

// Package secret implements the Secret business logic.
package secret

import (
	"context"
	"errors"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// SecretBiz is the Secret business logic.
type SecretBiz interface {
	Create(ctx context.Context, req *v1.CreateSecretRequest) (*v1.CreateSecretResponse, error)
	Get(ctx context.Context, req *v1.GetSecretRequest) (*v1.GetSecretResponse, error)
	List(ctx context.Context, req *v1.ListSecretRequest) (*v1.ListSecretResponse, error)
	Update(ctx context.Context, req *v1.UpdateSecretRequest) (*v1.UpdateSecretResponse, error)
	Delete(ctx context.Context, req *v1.DeleteSecretRequest) (*v1.DeleteSecretResponse, error)
	// Key resolves a secret ID to its key for request signing. It reports false
	// for an unknown or empty ID rather than returning an empty key, which would
	// still verify a signature computed with it.
	Key(ctx context.Context, secretID string) (string, bool)
	// RevealKey returns the stored key for the console's "查看" button. Get and
	// List stay key-free; this is the only read that carries the plaintext, and
	// the governance middleware restricts the route to administrators.
	RevealKey(ctx context.Context, secretID string) (string, error)
}

type secretBiz struct{ store agentstore.AIStore }

var _ SecretBiz = (*secretBiz)(nil)

// New creates a SecretBiz.
func New(store agentstore.AIStore) *secretBiz { return &secretBiz{store: store} }

// Key implements SecretBiz. The store is backed by the core Secret informer, so
// this is a local lookup rather than the database query it used to be.
func (b *secretBiz) Key(ctx context.Context, secretID string) (string, bool) {
	if secretID == "" || b.store == nil {
		return "", false
	}
	s, err := b.store.Secrets().Get(ctx, where.F("secret_id", secretID))
	if err != nil || s == nil || s.SecretKey == "" {
		return "", false
	}
	return s.SecretKey, true
}

// RevealKey implements SecretBiz.
//
// The value is stored in the core Secret and is returned as-is; unlike a model
// key there is no local encryption layer to open. Get and List keep returning
// an empty key, so a page load never carries every credential at once.
func (b *secretBiz) RevealKey(ctx context.Context, secretID string) (string, error) {
	if b.store == nil {
		return "", errno.ErrNotConfigured.WithMessage("服务未配置资源存储，无法读取凭据")
	}
	row, err := b.store.Secrets().Get(ctx, where.F("secret_id", secretID))
	if err != nil {
		return "", secretError(err, "reveal", secretID)
	}
	if row == nil {
		return "", errno.ErrNotFound.WithMessage("secret %q not found", secretID)
	}
	return row.SecretKey, nil
}

// Create implements SecretBiz.
func (b *secretBiz) Create(ctx context.Context, req *v1.CreateSecretRequest) (*v1.CreateSecretResponse, error) {
	row := conversion.SecretV1ToSecretModel(req.GetSecret())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.Secrets().Create(ctx, row); err != nil {
		return nil, secretError(err, "create", row.SecretID)
	}
	return &v1.CreateSecretResponse{Secret: conversion.SecretModelToSecretV1(row, true)}, nil
}

// Get implements SecretBiz.
func (b *secretBiz) Get(ctx context.Context, req *v1.GetSecretRequest) (*v1.GetSecretResponse, error) {
	row, err := b.store.Secrets().Get(ctx, where.F("secret_id", req.GetSecretId()))
	if err != nil {
		return nil, secretError(err, "get", req.GetSecretId())
	}
	if row == nil {
		return nil, errno.ErrNotFound.WithMessage("secret %q not found", req.GetSecretId())
	}
	return &v1.GetSecretResponse{Secret: conversion.SecretModelToSecretV1(row, false)}, nil
}

// List implements SecretBiz.
func (b *secretBiz) List(ctx context.Context, req *v1.ListSecretRequest) (*v1.ListSecretResponse, error) {
	total, list, err := b.store.Secrets().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, secretListError(err)
	}
	secrets := make([]*v1.Secret, 0, len(list))
	for _, row := range list {
		secrets = append(secrets, conversion.SecretModelToSecretV1(row, false))
	}
	return &v1.ListSecretResponse{Secrets: secrets, Count: total}, nil
}

// Update implements SecretBiz.
//
// The key is not returned by any read API, so an omitted one preserves the
// stored value. The description is always replaced.
func (b *secretBiz) Update(ctx context.Context, req *v1.UpdateSecretRequest) (*v1.UpdateSecretResponse, error) {
	s := req.GetSecret()
	row, err := b.store.Secrets().Get(ctx, where.F("secret_id", s.GetSecretId()))
	if err != nil {
		return nil, secretError(err, "update", s.GetSecretId())
	}
	if row == nil {
		return nil, errno.ErrNotFound.WithMessage("secret %q not found", s.GetSecretId())
	}
	if s.GetSecretKey() != "" {
		row.SecretKey = s.GetSecretKey()
	}
	row.Description = s.GetDescription()
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.Secrets().Update(ctx, row); err != nil {
		return nil, secretError(err, "update", s.GetSecretId())
	}
	return &v1.UpdateSecretResponse{Secret: conversion.SecretModelToSecretV1(row, false)}, nil
}

// Delete implements SecretBiz.
func (b *secretBiz) Delete(ctx context.Context, req *v1.DeleteSecretRequest) (*v1.DeleteSecretResponse, error) {
	if err := b.store.Secrets().Delete(ctx, where.F("secret_id", req.GetSecretId())); err != nil {
		return nil, secretError(err, "delete", req.GetSecretId())
	}
	return &v1.DeleteSecretResponse{}, nil
}

// secretError maps a control-plane write onto the API's error taxonomy.
func secretError(err error, verb, id string) error {
	switch {
	case apierrors.IsNotFound(err):
		return errno.ErrNotFound.WithMessage("secret %q not found", id)
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		return errno.ErrAlreadyExists.WithMessage("secret %q already exists or was changed concurrently", id)
	case apierrors.IsForbidden(err):
		return errno.ErrPermissionDenied.WithMessage("not allowed to %s secret %q", verb, id)
	case apierrors.IsUnauthorized(err):
		return errno.ErrUnauthenticated.WithMessage(
			"the control plane rejected the credentials for %s secret %q", verb, id,
		)
	case apierrors.IsInvalid(err):
		return errno.ErrInvalidArgument.WithMessage("secret %q is invalid: %v", id, err)
	}

	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		return errno.ErrInternal.WithMessage(
			"failed to %s secret %q in the control plane: %s",
			verb, id, statusErr.ErrStatus.Message,
		)
	}
	return storeerr.Write(err)
}

func secretListError(err error) error {
	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		return errno.ErrInternal.WithMessage("failed to list secrets: %s", statusErr.ErrStatus.Message)
	}
	return storeerr.Read(err)
}
