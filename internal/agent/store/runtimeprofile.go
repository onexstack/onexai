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

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
)

// RuntimeProfileStore persists runtime contracts.
type RuntimeProfileStore interface {
	Create(ctx context.Context, obj *model.RuntimeProfileM) error
	Update(ctx context.Context, obj *model.RuntimeProfileM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.RuntimeProfileM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.RuntimeProfileM, error)

	RuntimeProfileExpansion
}

// RuntimeProfileExpansion holds runtime-profile operations that are not the
// standard five.
type RuntimeProfileExpansion any
