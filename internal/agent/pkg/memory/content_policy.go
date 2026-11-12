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

package memory

import (
	"context"

	"github.com/onexstack/onexai/internal/agent/model"
)

// hiddenContent is what an administrator sees when a Memory has
// allowAdminContentRead off and they are reading someone else's bucket. It is
// deliberately explicit: an empty string would look like a missing entry.
const hiddenContent = "[内容已隐藏：该记忆库未开启 allowAdminContentRead]"

type callerContextKey struct{}

// WithCaller records the authenticated browser user on the request context.
// The binder never trusts a body field for this: the handler reads it from the
// token and overwrites whatever the client sent.
func WithCaller(ctx context.Context, caller string) context.Context {
	return context.WithValue(ctx, callerContextKey{}, caller)
}

// CallerFromContext returns the browser caller, or "" for a service call.
func CallerFromContext(ctx context.Context) string {
	caller, _ := ctx.Value(callerContextKey{}).(string)
	return caller
}

// RedactHiddenContent replaces the content of entries an administrator may not
// read. The entry itself stays visible so the administrator can still see that
// it exists, who owns it, and delete or purge it.
func RedactHiddenContent(entries ...*model.MemoryEntryM) {
	for _, entry := range entries {
		if entry != nil {
			entry.Content = hiddenContent
		}
	}
}
