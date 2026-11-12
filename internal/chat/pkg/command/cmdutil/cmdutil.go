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

// Package cmdutil holds helpers shared by the chat chat command packages.
package cmdutil

import (
	"context"
	"fmt"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// ResolveUsername resolves the identity of the caller to use as a Job/CronJob
// ownership key: the JWT user for a REST caller, the Lark user_id for somebody
// messaging the bot — one answer, from the one place that knows how to combine
// them. Returns a UserError when there is none, so the caller surfaces a clear
// message instead of silently attributing records to a shared bucket.
func ResolveUsername(ctx context.Context) (string, error) {
	if caller := runtime.From(ctx).Identity(); caller != "" {
		return caller, nil
	}
	return "", runtime.NewUserError("无法识别调用者身份，无法确定归属用户")
}

// ScopeToCaller builds a where.Options from the given key/value filters, always
// appending a "username" predicate pinned to the resolved caller. Pushing the
// ownership check into the SQL WHERE makes it atomic (no TOCTOU gap between a
// preliminary Get and the mutation) and means another user's record is simply
// invisible — reported as "not found" rather than leaking who owns it.
func ScopeToCaller(ctx context.Context, kvs ...any) (*where.Options, error) {
	caller, err := ResolveUsername(ctx)
	if err != nil {
		return nil, err
	}
	kvs = append(kvs, "username", caller)
	return where.F(kvs...), nil
}

// LoadShanghaiLocation returns the Asia/Shanghai location, falling back to
// time.Local only when the zone database entry is unavailable. Commands use it
// to stamp timestamps with an explicit +08:00 offset regardless of the
// process's own timezone (usually UTC in containers).
func LoadShanghaiLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Local
	}
	return loc
}

// ScopedStore is the subset of a resource store used by the GetScoped/DeleteScoped
// templates: it only exposes the operations those two commands share. JobStore
// and CronJobStore both satisfy it with T = their model type, so the get/delete
// command bodies can be shared (template method) instead of copy-pasted.
type ScopedStore[T any] interface {
	Get(ctx context.Context, opts *where.Options) (*T, error)
	Delete(ctx context.Context, opts *where.Options) error
}

// GetScoped fetches a single record owned by the current caller and sends it to
// out. idField is the primary-key column (e.g. "job_id"); resource names the
// resource in error text (e.g. "job").
func GetScoped[T any](ctx context.Context, st ScopedStore[T], idField, idValue, resource string, out runtime.Responder) error {
	scope, err := ScopeToCaller(ctx, idField, idValue)
	if err != nil {
		return err
	}
	obj, err := st.Get(ctx, scope)
	if err != nil {
		return fmt.Errorf("%s get: %s %q not found or you don't have permission: %w", resource, resource, idValue, err)
	}
	return out.SendResult(ctx, &runtime.CommandResult{Raw: obj})
}

// DeleteScoped deletes a single record owned by the current caller and reports
// success. resultKey is the JSON key carrying the deleted ID in the response.
func DeleteScoped[T any](ctx context.Context, st ScopedStore[T], idField, idValue, resource, resultKey string, out runtime.Responder) error {
	scope, err := ScopeToCaller(ctx, idField, idValue)
	if err != nil {
		return err
	}
	if err := st.Delete(ctx, scope); err != nil {
		return fmt.Errorf("%s delete: %s %q not found or you don't have permission: %w", resource, resource, idValue, err)
	}
	return out.SendResult(ctx, &runtime.CommandResult{Raw: map[string]any{resultKey: idValue, "deleted": true}})
}
