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

package store

import (
	"context"

	"github.com/onexstack/onexai/internal/agent/model"
)

// UserGroupStore is the persistence surface for console-managed user groups.
//
// The shape is purpose-built rather than the generic five methods: a group and
// its members are one object to every caller, and an update replaces the member
// set atomically. A generic Update over the group row alone would leave a
// half-applied membership list to a second caller that forgot a method.
type UserGroupStore interface {
	// Create persists a group and its members in one transaction.
	Create(ctx context.Context, group *model.UserGroupM) error
	// Update rewrites a group and replaces its member set in one transaction.
	// A group that does not exist is reported as ErrNotFound.
	Update(ctx context.Context, group *model.UserGroupM) error
	// Delete removes a group and its members. Absence is not an error.
	Delete(ctx context.Context, name string) error
	// Get returns one group with its members, or ErrNotFound.
	Get(ctx context.Context, name string) (*model.UserGroupM, error)
	// List returns one page of groups, newest first.
	List(ctx context.Context, opts ListUserGroupsOptions) (int64, []*model.UserGroupM, error)
	// GroupCodesFor returns the names of every enabled group that names
	// identity through any of its aliases. It is the hot path the authorization
	// resolver calls; the result is sorted and deduplicated.
	GroupCodesFor(ctx context.Context, identity string) ([]string, error)
}

// ListUserGroupsOptions narrows and pages a group list.
//
// Member matches any of the four member aliases exactly. Keyword searches name,
// display name and description. An empty field means "no filter".
type ListUserGroupsOptions struct {
	Offset  int
	Limit   int
	Member  string
	Keyword string
}
