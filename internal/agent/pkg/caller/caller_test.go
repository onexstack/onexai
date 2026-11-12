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

package caller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/authz/scope"
)

func userContext(id, name string) context.Context {
	return authz.ContextWithSubject(context.Background(), authz.SubjectFromUser(id, name))
}

// TestSubjectFilterIsTheCallerForAPerson pins the rule that keeps one list
// endpoint from showing another person's rows.
func TestSubjectFilterIsTheCallerForAPerson(t *testing.T) {
	ctx := userContext("u1", "someone")

	assert.Equal(t, "u1", SubjectFilter(ctx))
	assert.True(t, Scope(ctx).Matches(scope.Object{Subject: "u1"}))
	assert.False(t, Scope(ctx).Matches(scope.Object{Subject: "u2"}))
}

// TestSubjectFilterIsEmptyForAService pins that the platform's own callers see
// the whole collection: the console is the platform acting, not a person
// browsing.
func TestSubjectFilterIsEmptyForAService(t *testing.T) {
	ctx := authz.ContextWithSubject(context.Background(), authz.SubjectFromService("oai-chat"))

	assert.Empty(t, SubjectFilter(ctx))
	assert.True(t, Scope(ctx).AdmitsAll())
}

// TestSubjectFilterIsEmptyWithoutASubject pins the deployment with
// authentication bypassed: no caller means no boundary to enforce.
func TestSubjectFilterIsEmptyWithoutASubject(t *testing.T) {
	ctx := context.Background()

	assert.Empty(t, SubjectFilter(ctx))
	assert.True(t, Scope(ctx).AdmitsAll())
}

// TestMemoryOfAnotherSubjectIsInvisible pins that naming someone else's subject
// does not reach their memories — and is answered the same way as a subject with
// none, so the refusal does not confirm they exist.
func TestMemoryOfAnotherSubjectIsInvisible(t *testing.T) {
	ctx := userContext("u1", "someone")

	assert.True(t, Visible(ctx, "u1"))
	assert.False(t, Visible(ctx, "u2"))
}

func TestCallerOwnsItsOwnSession(t *testing.T) {
	assert.True(t, Owns(userContext("u1", "someone"), "u1"))
}

// A conversation that belongs to someone else is indistinguishable from one
// that does not exist: the decision is the same 404 the transport writes.
func TestCallerDoesNotOwnAnotherSession(t *testing.T) {
	assert.False(t, Owns(userContext("u1", "someone"), "u2"))
}

// A service acts on behalf of the platform and dispatches work for somebody
// else, so it may read the rows it produced. Without this the job controller
// could submit a scheduled run and then get "not found" for it.
func TestServiceOwnsEverySession(t *testing.T) {
	ctx := authz.ContextWithSubject(context.Background(), authz.SubjectFromService("oai-job-controller"))

	assert.True(t, Owns(ctx, "someone"))
}

// A deployment with authentication bypassed has no subject on the context, and
// refusing there would make it unable to read its own conversations.
func TestCallerWithoutASubjectIsAllowed(t *testing.T) {
	assert.True(t, Owns(context.Background(), "someone"))
}

// A row written before ownership was recorded has nothing to compare against.
func TestOwnerlessSessionIsAllowed(t *testing.T) {
	assert.True(t, Owns(userContext("u1", "someone"), ""))
}
