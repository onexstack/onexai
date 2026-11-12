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
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	"github.com/onexstack/onexstack/pkg/ptr"
)

// Profile is the contact record behind one open_id.
//
// UserID is the tenant-scoped Lark user_id, not onexai's account id. Empty
// fields mean "the app may not read this field" — a profile with a name but no
// user_id is a real, useful answer, so a lookup that returns one is not an
// error.
type Profile struct {
	UserID    string
	UnionID   string
	OpenID    string
	Name      string
	AvatarURL string
}

// ProfileResolver reads contact profiles by open_id, with a short-lived cache.
//
// It is deliberately not IdentityResolver: that one caches errors forever,
// which suits a one-shot backfill CLI and is wrong for a server that has to
// recover from a transient contact-API failure without a restart. The two
// share fetchContactUser, so the API contract lives in one place.
type ProfileResolver struct {
	cli   *lark.Client
	sleep time.Duration
	now   func() time.Time

	positiveTTL time.Duration
	negativeTTL time.Duration

	mu    sync.Mutex
	cache map[string]profileEntry
	calls int
}

type profileEntry struct {
	profile Profile
	err     error
	expires time.Time
}

// NewProfileResolver returns a resolver over the given client. sleep is the
// pause after each API call, used by the backfill to stay under the app's
// contact-API rate limit.
func NewProfileResolver(cli *lark.Client, sleep time.Duration) *ProfileResolver {
	return &ProfileResolver{
		cli:         cli,
		sleep:       sleep,
		now:         time.Now,
		positiveTTL: 10 * time.Minute,
		negativeTTL: time.Minute,
		cache:       map[string]profileEntry{},
	}
}

// Profile resolves openID to its contact record. A partial record (for example
// a name without a user_id) is returned without an error.
func (r *ProfileResolver) Profile(ctx context.Context, openID string) (Profile, error) {
	if r == nil || r.cli == nil {
		return Profile{}, fmt.Errorf("larkkit: no client to resolve %s with", openID)
	}
	if openID == "" {
		return Profile{}, fmt.Errorf("larkkit: empty open_id")
	}

	r.mu.Lock()
	if entry, ok := r.cache[openID]; ok && r.now().Before(entry.expires) {
		r.mu.Unlock()
		return entry.profile, entry.err
	}
	r.calls++
	r.mu.Unlock()

	profile, err := r.lookup(ctx, openID)

	ttl := r.positiveTTL
	if err != nil {
		ttl = r.negativeTTL
	}
	if ttl > 0 {
		r.mu.Lock()
		r.cache[openID] = profileEntry{profile: profile, err: err, expires: r.now().Add(ttl)}
		r.mu.Unlock()
	}
	return profile, err
}

func (r *ProfileResolver) lookup(ctx context.Context, openID string) (Profile, error) {
	user, err := fetchContactUser(ctx, r.cli, openID)
	if r.sleep > 0 {
		time.Sleep(r.sleep)
	}
	if err != nil {
		return Profile{}, err
	}

	profile := Profile{
		UserID:  ptr.From(user.UserId),
		UnionID: ptr.From(user.UnionId),
		OpenID:  ptr.From(user.OpenId),
		Name:    ptr.From(user.Name),
	}
	if user.Avatar != nil {
		for _, candidate := range []*string{user.Avatar.Avatar640, user.Avatar.Avatar240, user.Avatar.Avatar72} {
			if url := ptr.From(candidate); url != "" {
				profile.AvatarURL = url
				break
			}
		}
	}
	return profile, nil
}

// Calls reports how many lookups went to the API (cache misses).
func (r *ProfileResolver) Calls() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// fetchContactUser is the one place that talks to the contact API in the
// open_id namespace.
func fetchContactUser(ctx context.Context, cli *lark.Client, openID string) (*larkcontact.User, error) {
	req := larkcontact.NewGetUserReqBuilder().UserId(openID).UserIdType("open_id").Build()
	resp, err := cli.Contact.User.Get(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("contact get user %s: %w", openID, err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("contact get user %s: code=%d msg=%s", openID, resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.User == nil {
		return nil, fmt.Errorf("contact get user %s: empty response", openID)
	}
	return resp.Data.User, nil
}
