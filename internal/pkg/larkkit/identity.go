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

package larkkit

import (
	"context"
	"fmt"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/onexstack/onexstack/pkg/ptr"
)

// Identity is the id onexai keys a person by: the tenant-scoped Lark user_id
// ("go.kong"), not the per-application open_id. Authorizations, record
// ownership and allow-lists all name it, and it is the id a Lark API asked for
// user_id_type=user_id expects.
//
// It lives here rather than next to any one caller because the tools that
// translate stored ids need the same lookup the services use, and a second
// implementation of "which id is the identity" is how the two namespaces got
// mixed in the first place.

// IdentityResolver turns a Lark open_id into the user_id behind it, once per
// id, and remembers the answer either way.
//
// The lookup is the contact API's user read, asked in the open_id namespace:
// the response is the user object, whose user_id field is populated only when
// the app may read user IDs. An empty answer is therefore a real outcome and
// not an error in itself — callers decide whether to fall back or to report.
type IdentityResolver struct {
	cli   *lark.Client
	sleep time.Duration

	mu    sync.Mutex
	ids   map[string]string
	errs  map[string]error
	calls int
}

// NewIdentityResolver returns a resolver over the given client. sleep is the
// pause after each API call, to stay under the app's rate limit; a one-off
// backfill has nothing to gain from going faster into a throttling error.
func NewIdentityResolver(cli *lark.Client, sleep time.Duration) *IdentityResolver {
	return &IdentityResolver{
		cli:   cli,
		sleep: sleep,
		ids:   map[string]string{},
		errs:  map[string]error{},
	}
}

// UserID resolves openID to a user_id. The error is non-nil when the id could
// not be resolved, and the result is cached so a repeated id costs nothing.
func (r *IdentityResolver) UserID(ctx context.Context, openID string) (string, error) {
	if r == nil || r.cli == nil {
		return "", fmt.Errorf("larkkit: no client to resolve %s with", openID)
	}

	r.mu.Lock()
	if id, ok := r.ids[openID]; ok {
		err := r.errs[openID]
		r.mu.Unlock()
		return id, err
	}
	r.calls++
	r.mu.Unlock()

	id, err := r.lookup(ctx, openID)

	r.mu.Lock()
	r.ids[openID] = id
	r.errs[openID] = err
	r.mu.Unlock()
	return id, err
}

func (r *IdentityResolver) lookup(ctx context.Context, openID string) (string, error) {
	user, err := fetchContactUser(ctx, r.cli, openID)
	if r.sleep > 0 {
		time.Sleep(r.sleep)
	}
	if err != nil {
		return "", err
	}

	id := ptr.From(user.UserId)
	if id == "" {
		return "", fmt.Errorf("contact get user %s: no user_id (a departed employee, or the app may not read user IDs)", openID)
	}
	return id, nil
}

// Cached returns a resolution performed earlier in this run — the empty string
// when the id has not been looked up, or could not be resolved.
func (r *IdentityResolver) Cached(openID string) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ids[openID]
}

// Calls reports how many lookups went to the API (cache misses).
func (r *IdentityResolver) Calls() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}
