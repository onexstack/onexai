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

// Package caller answers what the caller may see, from the authenticated
// subject the transport put on the context. It is transport-agnostic so the
// business layer can make the same decision the HTTP edge used to make.
package caller

import (
	"context"

	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/authz/scope"
)

// Owns reports whether the caller may read or change a conversation's data.
//
// # Why an absent subject allows
//
// A deployment that runs with authentication bypassed has no subject on the
// context — that is the state every other surface treats as "no boundary to
// enforce", and refusing here would make the local deployment unable to read
// its own conversations. An ownerless row is the same case from the other side:
// it predates ownership being recorded, so there is nothing to compare against.
//
// # Why a service account allows
//
// A service is the platform acting, not a person browsing: it dispatches work
// on somebody else's behalf and then has to read the result. The job
// controller is exactly that caller — it submits a scheduled run as the
// schedule's owner and polls that run by id. Comparing the service's own name
// with the owner would answer "not found" for every run it just started, which
// is how a scheduled execution stalled after admission. Scope already answers
// this way for collections; Owns has to agree, or a service can list what it
// may not read.
func Owns(ctx context.Context, owner string) bool {
	subject, ok := authz.SubjectFromContext(ctx)
	if !ok || owner == "" {
		return true
	}
	if subject.Kind == authz.SubjectKindServiceAccount {
		return true
	}
	return subject.PrimaryID() == owner
}

// Scope is what the caller may see in a collection it did not name row by row.
//
// The rule is deliberately blunt: a person sees the rows they own, a service
// sees the platform's rows, and a deployment with authentication bypassed sees
// everything. It is one function so the answer cannot differ between two list
// endpoints — the failure mode that makes an "admin-only" table readable through
// the endpoint nobody remembered to fix.
func Scope(ctx context.Context) scope.Matcher {
	subject, ok := authz.SubjectFromContext(ctx)
	if !ok {
		return all()
	}
	if subject.Kind == authz.SubjectKindServiceAccount {
		return all()
	}
	id := subject.PrimaryID()
	if id == "" {
		// A subject with no identifier owns nothing; an empty value would match
		// every row whose owner was never recorded.
		return scope.Matcher{}
	}
	matcher, err := scope.Compile(scope.Scope{
		Kind: scope.AnyOf,
		AnyOf: []scope.ScopeClause{{
			All: []scope.ScopePredicate{{
				Attribute: scope.AttributeSubject,
				Operator:  scope.In,
				Values:    []string{id},
			}},
		}},
	})
	if err != nil {
		return scope.Matcher{}
	}
	return matcher
}

// SubjectFilter is the SQL-side counterpart of Scope: the subject a query must
// be restricted to, or "" when the caller may see every row.
func SubjectFilter(ctx context.Context) string {
	matcher := Scope(ctx)
	if matcher.AdmitsAll() {
		return ""
	}
	values, ok := matcher.Values(scope.AttributeSubject)
	if !ok || len(values) != 1 {
		// Not expressible as one subject: the caller may see nothing, which a
		// filter no row can match expresses.
		return "\x00none"
	}
	return values[0]
}

// Visible reports whether the caller may read a subject's memories.
//
// It is a function rather than a pair of lines inside the handler so the rule
// can be tested without a database: a memory belongs to one person, and the
// question "may this caller read that person's?" has nothing to do with storage.
func Visible(ctx context.Context, subject string) bool {
	return Scope(ctx).Matches(scope.Object{Subject: subject})
}

func all() scope.Matcher {
	allowed, err := scope.Compile(scope.Scope{Kind: scope.All})
	if err != nil {
		return scope.Matcher{}
	}
	return allowed
}
