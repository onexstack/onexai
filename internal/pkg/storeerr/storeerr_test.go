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

package storeerr

import (
	"errors"
	"net/http"
	"testing"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/onexstack/onexai/internal/pkg/errno"
)

type record struct {
	Name string
}

// The control-plane reader answers an absent row as (nil, nil), not as an
// error. A caller that inspected only the error therefore answered 200 with an
// empty envelope, which a service-to-service caller reads as "this build does
// not serve the route": onex's create-if-missing knowledge-base registration
// never created it, and that item's RAG sync failed on every retry.
func TestGetErrorAnswers404ForAnAbsentRow(t *testing.T) {
	var row *record

	err := GetError(row, nil, "knowledge base %q not found", "kb-x")
	assert.Equal(t, http.StatusNotFound, errorsx.FromError(err).Code)
	assert.Contains(t, errorsx.FromError(err).Message, `knowledge base "kb-x" not found`)
}

func TestGetErrorLeavesAPresentRowAlone(t *testing.T) {
	assert.NoError(t, GetError(&record{Name: "kb-x"}, nil,
		"knowledge base %q not found", "kb-x"))
}

func TestGetErrorKeepsReadFailuresDistinctFromAbsence(t *testing.T) {
	var row *record
	readErr := errors.New("indexer is not synced")

	// An unreadable store is an operational failure, not a missing row: turning
	// it into a 404 is what made this distinction worth keeping. It is also not
	// a raw error: the handler must not serialize the backend's text.
	err := GetError(row, readErr, "knowledge base %q not found", "kb-x")
	assert.ErrorIs(t, err, readErr)
	assert.ErrorIs(t, err, errno.ErrDBRead)
	assert.Equal(t, http.StatusInternalServerError, errorsx.FromError(err).Code)
}

func TestGetErrorAnswers404ForAKubernetesNotFound(t *testing.T) {
	var row *record
	notFound := apierrors.NewNotFound(schema.GroupResource{Resource: "commands"}, "cmd-x")

	err := GetError(row, notFound, "command %q not found", "cmd-x")

	assert.Equal(t, http.StatusNotFound, errorsx.FromError(err).Code)
	assert.Contains(t, errorsx.FromError(err).Message, `command "cmd-x" not found`)
}

func TestGetErrorPassesANotFoundThrough(t *testing.T) {
	var row *record

	err := GetError(row, gorm.ErrRecordNotFound, "knowledge base %q not found", "kb-x")
	assert.Equal(t, http.StatusNotFound, errorsx.FromError(err).Code)
}

func TestReadPreservesTheCause(t *testing.T) {
	cause := errors.New("connection reset")

	err := Read(cause)

	assert.ErrorIs(t, err, cause)
	assert.Equal(t, http.StatusInternalServerError, errorsx.FromError(err).Code)
	assert.NoError(t, Read(nil))
}

func TestWritePreservesTheCause(t *testing.T) {
	cause := errors.New("duplicate key")

	err := Write(cause)

	assert.ErrorIs(t, err, cause)
	assert.Equal(t, http.StatusInternalServerError, errorsx.FromError(err).Code)
	assert.NoError(t, Write(nil))
}

// A control-plane store reports "already exists", "forbidden" and friends as a
// Kubernetes StatusError. Reporting every one of them as InternalError.DBWrite
// turned an actionable refusal into a generic 500.
func TestWriteClassifiesKubernetesStatusErrors(t *testing.T) {
	cases := []struct {
		name   string
		cause  error
		code   int
		target error
	}{
		{
			name:   "already exists",
			cause:  apierrors.NewAlreadyExists(schema.GroupResource{Resource: "agents"}, "a1"),
			code:   http.StatusConflict,
			target: errno.ErrAlreadyExists,
		},
		{
			name:   "not found",
			cause:  apierrors.NewNotFound(schema.GroupResource{Resource: "agents"}, "a1"),
			code:   http.StatusNotFound,
			target: errno.ErrNotFound,
		},
		{
			name: "invalid",
			cause: apierrors.NewInvalid(
				schema.GroupKind{Group: "agent.onexai.io", Kind: "Agent"}, "a1",
				field.ErrorList{field.Required(field.NewPath("spec"), "required")},
			),
			code:   http.StatusBadRequest,
			target: errno.ErrInvalidArgument,
		},
		{
			name:   "forbidden",
			cause:  apierrors.NewForbidden(schema.GroupResource{Resource: "agents"}, "a1", errors.New("denied")),
			code:   http.StatusForbidden,
			target: errno.ErrPermissionDenied,
		},
		{
			name:   "unauthorized",
			cause:  apierrors.NewUnauthorized("no token"),
			code:   http.StatusUnauthorized,
			target: errno.ErrUnauthenticated,
		},
		{
			name:   "other status",
			cause:  apierrors.NewInternalError(errors.New("etcd is unhappy")),
			code:   http.StatusInternalServerError,
			target: errno.ErrInternal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Write(tc.cause)

			assert.ErrorIs(t, err, tc.target)
			assert.ErrorIs(t, err, tc.cause) // Wrap keeps the control-plane cause
			assert.Equal(t, tc.code, errorsx.FromError(err).Code)
		})
	}
}

// Ordinary SQL/driver failures are not StatusErrors and must stay DBWrite.
func TestWriteKeepsSQLErrorsAsDBWrite(t *testing.T) {
	cause := errors.New(`ERROR: duplicate key value violates unique constraint "idx_chat_oncall_group_name" (SQLSTATE 23505)`)

	err := Write(cause)

	assert.ErrorIs(t, err, errno.ErrDBWrite)
	assert.NotErrorIs(t, err, errno.ErrInternal)
	assert.ErrorIs(t, err, cause)
	assert.Equal(t, http.StatusInternalServerError, errorsx.FromError(err).Code)
}
