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

// Package command serves the read-only view of the platform command catalogue.
//
// A command's body is compiled into the chat binary; the Command object is only
// its catalogue entry, reconciled by internal/chat/commandreg. There is
// deliberately no write path here: an edit or delete would be undone by the
// next reconcile. User-defined commands are CustomCommand, which the agent
// service owns.
package command

import (
	"context"
	"sort"

	"github.com/google/wire"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// ProviderSet defines the Wire providers for the command biz layer.
var ProviderSet = wire.NewSet(New, wire.Bind(new(CommandBiz), new(*commandBiz)))

// CommandBiz exposes the read-only command catalogue.
type CommandBiz interface {
	// Get returns one command by name. An empty namespace searches every
	// namespace and returns the first match.
	Get(ctx context.Context, req *api.GetCommandRequest) (*v1.GetCommandResponse, error)
	// List returns a page of the catalogue and the total count.
	List(ctx context.Context, req *v1.ListCommandRequest) (*v1.ListCommandResponse, error)
}

type commandBiz struct {
	client *declclient.Client
}

// New creates the command biz.
func New(client *declclient.Client) *commandBiz {
	return &commandBiz{client: client}
}

// Ensure the concrete type implements the interface at compile time.
var _ CommandBiz = (*commandBiz)(nil)

func (b *commandBiz) Get(_ context.Context, req *api.GetCommandRequest) (*v1.GetCommandResponse, error) {
	if b.client == nil {
		return nil, errno.ErrNotConfigured
	}
	name, namespace := req.Name, req.Namespace
	if namespace != "" {
		item, err := b.client.Commands().Commands(namespace).Get(name)
		if err := storeerr.GetError(item, err, "command %q not found", name); err != nil {
			return nil, err
		}
		return &v1.GetCommandResponse{Command: commandToProto(item)}, nil
	}
	items, err := b.client.Commands().List(labels.Everything())
	if err != nil {
		return nil, storeerr.Read(err)
	}
	for _, item := range items {
		if item.Name == name {
			return &v1.GetCommandResponse{Command: commandToProto(item)}, nil
		}
	}
	return nil, errno.ErrNotFound.WithMessage("command %q not found", name)
}

func (b *commandBiz) List(_ context.Context, req *v1.ListCommandRequest) (*v1.ListCommandResponse, error) {
	if b.client == nil {
		return nil, errno.ErrNotConfigured
	}
	namespace := req.GetNamespace()
	offset, limit := int(req.GetOffset()), int(req.GetLimit())

	var items []*chatv1alpha1.Command
	if namespace != "" {
		found, err := b.client.Commands().Commands(namespace).List(labels.Everything())
		if err != nil {
			return nil, storeerr.Read(err)
		}
		items = found
	} else {
		// The catalogue is published into every namespace a binding resolves
		// commands from, so the same entry appears many times. A caller asking
		// for the catalogue wants one row per command, not one per namespace.
		all, err := b.client.Commands().List(labels.Everything())
		if err != nil {
			return nil, storeerr.Read(err)
		}
		seen := make(map[string]struct{}, len(all))
		for _, item := range all {
			if _, ok := seen[item.Name]; ok {
				continue
			}
			seen[item.Name] = struct{}{}
			items = append(items, item)
		}
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	total := int64(len(items))
	page := pageOf(items, offset, limit)
	out := make([]*v1.Command, 0, len(page))
	for _, item := range page {
		out = append(out, commandToProto(item))
	}
	return &v1.ListCommandResponse{Commands: out, Count: total}, nil
}

// pageOf applies the API's paging contract: limit zero means no limit, and an
// offset past the end is an empty page rather than an error.
func pageOf(items []*chatv1alpha1.Command, offset, limit int) []*chatv1alpha1.Command {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return nil
	}
	items = items[offset:]
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}

func commandToProto(item *chatv1alpha1.Command) *v1.Command {
	if item == nil {
		return nil
	}
	return &v1.Command{
		Name:         item.Name,
		Namespace:    item.Namespace,
		DisplayName:  item.Spec.DisplayName,
		Description:  item.Spec.Description,
		Enabled:      item.Spec.Enabled,
		Group:        item.Spec.Group,
		Aliases:      item.Spec.Aliases,
		Usage:        item.Spec.Usage,
		Source:       item.Spec.Source,
		AccountLevel: item.Spec.AccountLevel,
		Labels:       item.Labels,
		CreatedAt:    item.CreationTimestamp.UnixMilli(),
		UpdatedAt:    item.CreationTimestamp.UnixMilli(),
	}
}
