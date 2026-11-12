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

package customcommand

import (
	"context"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzview"
	"github.com/onexstack/onexai/internal/agent/pkg/customcmd"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// Create implements IBiz.
func (b *biz) Create(
	ctx context.Context, req *v1.CreateCustomCommandRequest,
) (*v1.CreateCustomCommandResponse, error) {
	row, err := customcmd.ProtoToModel(req.GetCustomCommand())
	if err != nil {
		return nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}
	now := time.Now().UnixMilli()
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := b.deps.Resources.CustomCommands().Create(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	// The tool set of every agent that has this command bound changed. Drop the
	// MCP discovery cache so the next run sees the new command instead of
	// serving the old list for the rest of the TTL.
	b.deps.MCP.Invalidate()
	out, err := customcmd.ModelToProto(row)
	if err != nil {
		return nil, errno.ErrInternal.WithMessage("%s", err.Error())
	}
	return &v1.CreateCustomCommandResponse{CustomCommand: out}, nil
}

// Get implements IBiz.
func (b *biz) Get(
	ctx context.Context, req *v1.GetCustomCommandRequest,
) (*v1.GetCustomCommandResponse, error) {
	row, err := b.deps.Resources.CustomCommands().Get(ctx, where.F("name", req.GetName()))
	if err := storeerr.GetError(row, err, "custom command %q not found", req.GetName()); err != nil {
		return nil, err
	}
	out, convErr := customcmd.ModelToProto(row)
	if convErr != nil {
		return nil, errno.ErrInternal.WithMessage("%s", convErr.Error())
	}
	return &v1.GetCustomCommandResponse{CustomCommand: out}, nil
}

// List implements IBiz.
func (b *biz) List(
	ctx context.Context, req *v1.ListCustomCommandRequest,
) (*v1.ListCustomCommandResponse, error) {
	total, list, err := b.deps.Resources.CustomCommands().List(ctx,
		pagination.Where(int(req.GetOffset()), int(req.GetLimit())))
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if authzview.ReadFiltered(ctx, "customcommand") {
		before := len(list)
		list = authzview.Filter(ctx, "customcommand", list,
			func(row *agentmodel.CustomCommandM) (string, string, string) {
				return row.Namespace, row.Name, ""
			})
		total = authzview.AdjustCount(total, before-len(list))
	}
	commands := make([]*v1.CustomCommand, 0, len(list))
	for _, row := range list {
		out, convErr := customcmd.ModelToProto(row)
		if convErr != nil {
			return nil, errno.ErrInternal.WithMessage("%s", convErr.Error())
		}
		commands = append(commands, out)
	}
	return &v1.ListCustomCommandResponse{CustomCommands: commands, Count: total}, nil
}

// Update implements IBiz.
func (b *biz) Update(
	ctx context.Context, req *v1.UpdateCustomCommandRequest,
) (*v1.UpdateCustomCommandResponse, error) {
	row, err := customcmd.ProtoToModel(req.GetCustomCommand())
	if err != nil {
		return nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	}
	row.UpdatedAt = time.Now().UnixMilli()
	if err := b.deps.Resources.CustomCommands().Update(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	// A schema/alias/type change is a tool change for every bound agent.
	b.deps.MCP.Invalidate()
	out, err := customcmd.ModelToProto(row)
	if err != nil {
		return nil, errno.ErrInternal.WithMessage("%s", err.Error())
	}
	return &v1.UpdateCustomCommandResponse{CustomCommand: out}, nil
}

// Delete implements IBiz.
func (b *biz) Delete(
	ctx context.Context, req *v1.DeleteCustomCommandRequest,
) (*v1.DeleteCustomCommandResponse, error) {
	if err := b.deps.Resources.CustomCommands().Delete(ctx, where.F("name", req.GetName())); err != nil {
		return nil, storeerr.Write(err)
	}
	b.deps.MCP.Invalidate()
	return &v1.DeleteCustomCommandResponse{}, nil
}
