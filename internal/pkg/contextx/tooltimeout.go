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

package contextx

import "context"

// toolTimeoutKey marks a context whose deadline the tool-call middleware
// installed for one invocation, as opposed to the run's own deadline.
//
// The distinction matters to whoever attributes a timeout: a tool whose server
// stopped responding is a fact about that server, while a run that ran out of
// time says nothing about it. Both surface as context.DeadlineExceeded, and the
// context is the only thing that tells them apart.
const toolTimeoutKey contextKey = "toolTimeout"

// WithToolTimeout marks ctx as carrying a per-tool-call deadline.
func WithToolTimeout(ctx context.Context) context.Context {
	return context.WithValue(ctx, toolTimeoutKey, true)
}

// IsToolTimeout reports whether ctx carries a per-tool-call deadline.
func IsToolTimeout(ctx context.Context) bool {
	marked, _ := ctx.Value(toolTimeoutKey).(bool)
	return marked
}
