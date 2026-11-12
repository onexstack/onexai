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

package user

import (
	"context"
	"log/slog"
	"time"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/store"
)

const (
	// larkProfileTimeout bounds the contact lookup on the bind path. The
	// handshake has already succeeded; a slow Feishu API must not turn it into
	// a failed request.
	larkProfileTimeout = 5 * time.Second
	// DefaultProfileBackfillDelay keeps the startup backfill under the app's
	// contact-API rate limit.
	DefaultProfileBackfillDelay = 200 * time.Millisecond
)

// LarkProfileResolver resolves the Feishu contact record behind an open_id.
// It is an interface so the biz layer does not depend on the Lark SDK.
type LarkProfileResolver interface {
	Profile(ctx context.Context, openID string) (model.LarkProfile, error)
}

// WithProfileResolver attaches the resolver without changing the constructors,
// so existing callers and tests keep compiling.
func (b *userBiz) WithProfileResolver(resolver LarkProfileResolver) *userBiz {
	if b != nil {
		b.profile = resolver
	}
	return b
}

func (b *userBiz) resolveProfile(ctx context.Context, openID string) (model.LarkProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, larkProfileTimeout)
	defer cancel()
	return b.profile.Profile(ctx, openID)
}

// RunLarkProfileBackfill fills the profile columns of bindings written before
// FeishuBindConfirm resolved them.
//
// It is best-effort: a failed row only logs, and the next start retries it.
// The loop is ctx-aware because the caller runs it in a goroutine tied to the
// server's signal-aware context.
func RunLarkProfileBackfill(
	ctx context.Context, users store.UserStore, resolver LarkProfileResolver, delay time.Duration,
) {
	if users == nil || resolver == nil {
		return
	}

	bindings, err := users.ListLarkBindings(ctx)
	if err != nil {
		slog.WarnContext(ctx, "user: cannot list Feishu bindings for the profile backfill", "error", err)
		return
	}

	var scanned, updated, unresolved int
	for _, binding := range bindings {
		if ctx.Err() != nil {
			return
		}
		if binding == nil || binding.OpenID == "" {
			continue
		}
		// A row with both names filled has nothing left to repair. A row with
		// one of them missing still benefits from the lookup.
		if binding.LarkUserID != "" && binding.Name != "" {
			continue
		}

		scanned++
		callCtx, cancel := context.WithTimeout(ctx, larkProfileTimeout)
		profile, profileErr := resolver.Profile(callCtx, binding.OpenID)
		cancel()

		switch {
		case profileErr != nil:
			unresolved++
			slog.WarnContext(ctx, "user: Feishu profile backfill could not resolve an open_id",
				"user_id", binding.UserID, "open_id", binding.OpenID, "error", profileErr)
		case profile.LarkUserID == "" && profile.UnionID == "" &&
			profile.Name == "" && profile.AvatarURL == "":
			unresolved++
		default:
			if err := users.UpdateLarkBindingProfile(ctx, binding.UserID, binding.OpenID, profile); err != nil {
				slog.WarnContext(ctx, "user: Feishu profile backfill write failed",
					"user_id", binding.UserID, "open_id", binding.OpenID, "error", err)
			} else {
				updated++
			}
		}

		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}

	slog.InfoContext(ctx, "user: Feishu profile backfill finished",
		"scanned", scanned, "updated", updated, "unresolved", unresolved)
}
