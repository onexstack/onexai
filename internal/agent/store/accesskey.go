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

// AccessKeyStore is the persistence surface for personal AccessKey/SecretKey
// pairs.
//
// # The owner is always part of the predicate
//
// A key belongs to the account that created it, and there is no
// administrator-reads-another-account's-keys route. Every method therefore
// takes the owner and writes it into the same WHERE as the identifier; there is
// no way to reach a row without saying whose it is.
type AccessKeyStore interface {
	// Create persists a new key.
	Create(ctx context.Context, key *model.AccessKeyM) error
	// Get returns one of the user's keys, or gorm.ErrRecordNotFound.
	Get(ctx context.Context, userID, id string) (*model.AccessKeyM, error)
	// List returns the user's keys, newest first.
	List(ctx context.Context, userID string) ([]*model.AccessKeyM, error)
	// Count returns how many keys the user holds, disabled ones included.
	Count(ctx context.Context, userID string) (int64, error)
	// SetEnabled turns one of the user's keys on or off. A key that does not
	// belong to the user is reported as ErrNotFound, never silently updated.
	SetEnabled(ctx context.Context, userID, id string, enabled bool) error
	// Delete removes one of the user's keys. Absence is not an error.
	Delete(ctx context.Context, userID, id string) error
}
