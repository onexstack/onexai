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
	"errors"
)

// ErrTxUnsupported is returned by a store whose backend cannot group writes
// into a transaction. It is an error rather than a silent pass-through: a
// caller that asked for atomicity and did not get it has to know, because the
// alternative is a half-applied write that looks like it succeeded.
var ErrTxUnsupported = errors.New("the configured resource store backend does not support transactions")

// TxStore is implemented by resource stores whose writes can be grouped into
// one transaction.
//
// It is a separate interface rather than a method on AIStore because not
// every store can honour it: the empty store (a deployment with no backend
// configured) has nothing to roll back, and making it part of the aggregate
// interface would force it to pretend. A caller that needs atomicity asserts
// for this interface and handles the absence.
type TxStore interface {
	// TX runs fn inside one transaction. The context passed to fn carries the
	// transaction, so a write reached through it is part of the same unit.
	TX(ctx context.Context, fn func(ctx context.Context) error) error
}
