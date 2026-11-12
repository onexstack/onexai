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

// Package namespace implements the business-line (namespace) business logic.
package namespace

import (
	"context"
	"errors"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

// NamespaceBiz is the business-line business logic.
type NamespaceBiz interface {
	Create(ctx context.Context, req *v1.CreateNamespaceRequest) (*v1.CreateNamespaceResponse, error)
	Get(ctx context.Context, req *v1.GetNamespaceRequest) (*v1.GetNamespaceResponse, error)
	List(ctx context.Context, req *v1.ListNamespaceRequest) (*v1.ListNamespaceResponse, error)
	Update(ctx context.Context, req *v1.UpdateNamespaceRequest) (*v1.UpdateNamespaceResponse, error)
	Delete(ctx context.Context, req *v1.DeleteNamespaceRequest) (*v1.DeleteNamespaceResponse, error)
}

type namespaceBiz struct{ store agentstore.AIStore }

var _ NamespaceBiz = (*namespaceBiz)(nil)

// New creates a NamespaceBiz.
func New(store agentstore.AIStore) *namespaceBiz { return &namespaceBiz{store: store} }

// Create implements NamespaceBiz.
func (b *namespaceBiz) Create(ctx context.Context, req *v1.CreateNamespaceRequest) (*v1.CreateNamespaceResponse, error) {
	row := conversion.NamespaceV1ToNamespaceModel(req.GetNamespace())
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.store.Namespaces().Create(ctx, row); err != nil {
		return nil, namespaceError(err, "create", row.Name)
	}
	return &v1.CreateNamespaceResponse{Namespace: conversion.NamespaceModelToNamespaceV1(row)}, nil
}

// Get implements NamespaceBiz.
func (b *namespaceBiz) Get(ctx context.Context, req *v1.GetNamespaceRequest) (*v1.GetNamespaceResponse, error) {
	row, err := b.store.Namespaces().Get(ctx, where.F("name", req.GetName()))
	if err != nil {
		return nil, namespaceError(err, "get", req.GetName())
	}
	if row == nil {
		return nil, errno.ErrNotFound.WithMessage("namespace %q not found", req.GetName())
	}
	return &v1.GetNamespaceResponse{Namespace: conversion.NamespaceModelToNamespaceV1(row)}, nil
}

// List implements NamespaceBiz.
func (b *namespaceBiz) List(ctx context.Context, req *v1.ListNamespaceRequest) (*v1.ListNamespaceResponse, error) {
	total, list, err := b.store.Namespaces().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, namespaceListError(err)
	}
	if authzview.ReadFiltered(ctx, "namespace") {
		before := len(list)
		list = authzview.Filter(ctx, "namespace", list, func(row *model.NamespaceM) (string, string, string) {
			return "", row.Name, row.Owner
		})
		total = authzview.AdjustCount(total, before-len(list))
	}
	namespaces := make([]*v1.Namespace, 0, len(list))
	for _, row := range list {
		namespaces = append(namespaces, conversion.NamespaceModelToNamespaceV1(row))
	}
	return &v1.ListNamespaceResponse{
		Namespaces:       namespaces,
		Count:            total,
		DefaultNamespace: sharing.DefaultNamespace,
	}, nil
}

// Update implements NamespaceBiz.
//
// The business line is read first so the fields the request does not carry (the
// creation time) survive the full-row write. The name is the identity and is
// never taken from the payload.
func (b *namespaceBiz) Update(ctx context.Context, req *v1.UpdateNamespaceRequest) (*v1.UpdateNamespaceResponse, error) {
	in := req.GetNamespace()
	row, err := b.store.Namespaces().Get(ctx, where.F("name", in.GetName()))
	if err != nil {
		return nil, namespaceError(err, "update", in.GetName())
	}
	if row == nil {
		return nil, errno.ErrNotFound.WithMessage("namespace %q not found", in.GetName())
	}
	row.DisplayName = in.GetDisplayName()
	row.Description = in.GetDescription()
	row.Owner = in.GetOwner()
	row.Labels = model.StringMap(in.GetLabels())
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.store.Namespaces().Update(ctx, row); err != nil {
		return nil, namespaceError(err, "update", in.GetName())
	}
	return &v1.UpdateNamespaceResponse{Namespace: conversion.NamespaceModelToNamespaceV1(row)}, nil
}

// Delete implements NamespaceBiz.
func (b *namespaceBiz) Delete(ctx context.Context, req *v1.DeleteNamespaceRequest) (*v1.DeleteNamespaceResponse, error) {
	if err := b.store.Namespaces().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, namespaceError(err, "delete", req.GetName())
	}
	return &v1.DeleteNamespaceResponse{}, nil
}

// namespaceError maps a control-plane write onto the API's error taxonomy.
//
// The store talks to the Kubernetes API directly, so duplicates, missing
// objects and policy denials arrive as StatusErrors. Wrapping every error with
// storeerr.Write made them look like database failures; only non-StatusErrors
// (a nil client, a transport failure the client did not classify) are database
// failures.
func namespaceError(err error, verb, name string) error {
	switch {
	case apierrors.IsNotFound(err):
		return errno.ErrNotFound.WithMessage("namespace %q not found", name)
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		return errno.ErrAlreadyExists.WithMessage("namespace %q already exists or was changed concurrently", name)
	case apierrors.IsForbidden(err):
		return errno.ErrPermissionDenied.WithMessage("not allowed to %s namespace %q", verb, name)
	case apierrors.IsUnauthorized(err):
		return errno.ErrUnauthenticated.WithMessage(
			"the control plane rejected the credentials for %s namespace %q", verb, name,
		)
	case apierrors.IsInvalid(err):
		return errno.ErrInvalidArgument.WithMessage("namespace %q is invalid: %v", name, err)
	}

	// Other Kubernetes StatusErrors (timeout, server error) are still
	// control-plane failures, not PostgreSQL failures.
	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		return errno.ErrInternal.WithMessage(
			"failed to %s namespace %q in the control plane: %s",
			verb, name, statusErr.ErrStatus.Message,
		)
	}

	// Non-APIStatus errors: no control-plane client, or a transport failure the
	// client left unclassified.
	return storeerr.Write(err)
}

// namespaceListError maps a list failure. There is no single name to report.
func namespaceListError(err error) error {
	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		return errno.ErrInternal.WithMessage("failed to list namespaces: %s", statusErr.ErrStatus.Message)
	}
	return storeerr.Read(err)
}
