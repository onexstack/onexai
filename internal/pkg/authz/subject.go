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

package authz

import "context"

// subjectContextKey is an unexported type so no other package can collide
// with this context key.
type subjectContextKey struct{}

// ContextWithSubject attaches subj to ctx for later retrieval by
// SubjectFromContext. Call sites that already resolved a Subject (the AK/SK
// boundary attaches the calling service; AgentBiz.Ask attaches the end user
// right before Engine.Run) should inject it into the context passed to
// adk.Runner.Run, so the tool-call middleware — running inside a Runner that
// may be shared/cached across many different callers — authorizes the actual
// caller of *this* invocation rather than whichever caller happened to
// trigger the Runner's construction.
func ContextWithSubject(ctx context.Context, subj Subject) context.Context {
	return context.WithValue(ctx, subjectContextKey{}, subj)
}

// SubjectFromContext retrieves the Subject attached by ContextWithSubject.
// Returns a zero-value Subject (Kind == "") and false if none was attached —
// callers must treat that as "unknown caller", not silently default to an
// implicitly-trusted identity.
func SubjectFromContext(ctx context.Context) (Subject, bool) {
	subj, ok := ctx.Value(subjectContextKey{}).(Subject)
	return subj, ok
}

// SubjectFromUser builds a Subject representing the human who initiated an
// agent run.
//
// userID is the caller's stable identity as the front-end reports it: for Lark
// traffic that is the tenant user_id (e.g. "go.kong"), which is what a rule's
// subject_id is written against. userName is optional and only used for logs.
func SubjectFromUser(userID, userName string) Subject {
	return Subject{
		Kind:     SubjectKindUser,
		UserID:   userID,
		UserName: userName,
	}
}

// SubjectFromService builds a Subject representing an internal, non-human
// caller identified only by a service name (in practice the AK/SK key id that
// signed the request).
func SubjectFromService(serviceName string) Subject {
	return Subject{Kind: SubjectKindServiceAccount, ServiceName: serviceName}
}
