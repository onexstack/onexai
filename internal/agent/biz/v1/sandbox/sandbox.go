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

// Package sandbox implements the sandbox administration logic.
package sandbox

import (
	"context"
	"errors"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	sandboxsvc "github.com/onexstack/onexai/internal/agent/pkg/sandbox"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// SandboxBiz is the sandbox administration logic.
type SandboxBiz interface {
	Create(ctx context.Context, req *v1.CreateSandboxRequest) (*CreateResponse, error)
	Get(ctx context.Context, req *v1.GetSandboxRequest) (*GetResponse, error)
	Delete(ctx context.Context, req *v1.DeleteSandboxRequest) error
	Exec(ctx context.Context, req *v1.ExecSandboxRequest) (*sandboxsvc.ExecResult, error)
	ReadFile(ctx context.Context, req *v1.ReadSandboxFileRequest) (*ReadFileResponse, error)
	WriteFile(ctx context.Context, req *v1.WriteSandboxFileRequest) error
}

// CreateResponse carries a new sandbox.
type CreateResponse struct {
	Sandbox *model.SandboxM `json:"sandbox"`
}

// GetResponse carries one sandbox.
type GetResponse struct {
	Sandbox *model.SandboxM `json:"sandbox"`
}

// ReadFileResponse carries a file's content.
type ReadFileResponse struct {
	Content string `json:"content"`
}

type sandboxBiz struct {
	service *sandboxsvc.Service
}

var _ SandboxBiz = (*sandboxBiz)(nil)

// New creates a SandboxBiz. A nil service makes every method refuse with "not
// configured".
func New(service *sandboxsvc.Service) *sandboxBiz { return &sandboxBiz{service: service} }

// Create implements SandboxBiz.
func (b *sandboxBiz) Create(
	ctx context.Context, req *v1.CreateSandboxRequest,
) (*CreateResponse, error) {
	if b.service == nil {
		return nil, errno.ErrNotFound.WithMessage("sandbox is not configured")
	}
	created, err := b.service.Create(ctx, req.GetOwner(), req.GetTemplate(),
		time.Duration(req.GetTtlSeconds())*time.Second)
	if err != nil {
		return nil, sandboxError(err)
	}
	return &CreateResponse{Sandbox: created}, nil
}

// Get implements SandboxBiz.
func (b *sandboxBiz) Get(ctx context.Context, req *v1.GetSandboxRequest) (*GetResponse, error) {
	if b.service == nil {
		return nil, errno.ErrNotFound.WithMessage("sandbox is not configured")
	}
	row, err := b.service.Get(ctx, req.GetId(), req.GetOwner())
	if err != nil {
		return nil, sandboxError(err)
	}
	return &GetResponse{Sandbox: row}, nil
}

// Delete implements SandboxBiz.
func (b *sandboxBiz) Delete(ctx context.Context, req *v1.DeleteSandboxRequest) error {
	if b.service == nil {
		return errno.ErrNotFound.WithMessage("sandbox is not configured")
	}
	if err := b.service.Delete(ctx, req.GetId(), req.GetOwner()); err != nil {
		return sandboxError(err)
	}
	return nil
}

// Exec implements SandboxBiz.
func (b *sandboxBiz) Exec(
	ctx context.Context, req *v1.ExecSandboxRequest,
) (*sandboxsvc.ExecResult, error) {
	if b.service == nil {
		return nil, errno.ErrNotFound.WithMessage("sandbox is not configured")
	}
	result, err := b.service.Exec(ctx, req.GetId(), req.GetOwner(), req.GetCommand(),
		time.Duration(req.GetTimeoutSeconds())*time.Second)
	if err != nil {
		return nil, sandboxError(err)
	}
	return result, nil
}

// ReadFile implements SandboxBiz.
func (b *sandboxBiz) ReadFile(
	ctx context.Context, req *v1.ReadSandboxFileRequest,
) (*ReadFileResponse, error) {
	if b.service == nil {
		return nil, errno.ErrNotFound.WithMessage("sandbox is not configured")
	}
	content, err := b.service.ReadFile(ctx, req.GetId(), req.GetOwner(), req.GetPath(), 0)
	if err != nil {
		return nil, sandboxError(err)
	}
	return &ReadFileResponse{Content: content}, nil
}

// WriteFile implements SandboxBiz.
func (b *sandboxBiz) WriteFile(ctx context.Context, req *v1.WriteSandboxFileRequest) error {
	if b.service == nil {
		return errno.ErrNotFound.WithMessage("sandbox is not configured")
	}
	if err := b.service.WriteFile(ctx, req.GetId(), req.GetOwner(), req.GetPath(), req.GetContent()); err != nil {
		return sandboxError(err)
	}
	return nil
}

func sandboxError(err error) error {
	if errors.Is(err, sandboxsvc.ErrNotFound) {
		return errno.ErrNotFound.WithMessage("sandbox not found")
	}
	return errno.ErrOperationFailed.WithMessage("%s", err.Error())
}
