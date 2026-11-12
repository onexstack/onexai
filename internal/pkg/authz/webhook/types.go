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

package webhook

// FailureMode determines the authz.Decision used when the webhook call itself
// fails (timeout, network error, non-2xx, malformed body) or returns a
// status.evaluationError.
type FailureMode string

const (
	// FailureModeNoOpinion (recommended default): timeout/failure maps to
	// NoOpinion, deferring to whatever else is Union'd alongside this
	// Authorizer (or to the caller's fail-closed default if this is the only
	// one).
	FailureModeNoOpinion FailureMode = "no_opinion"
	// FailureModeDeny maps timeout/failure to Deny — for high-risk deployments
	// where this webhook is the sole authority and a "cannot reach it" outcome
	// must never be treated as silently permissive.
	FailureModeDeny FailureMode = "deny"
)
