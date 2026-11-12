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

// Package restbind supplies the binders the REST handlers hand to
// core.HandleRequest.
//
// Generated request messages declare their path fields with
// `@gotags: uri:"..."` (see pkg/api/*/v1/*.proto), so a handler that reads
// only the path uses core.HandleUriRequest directly. These binders cover the
// two cases gin's tag binding cannot:
//
//   - request also carries a body: the path must win over the body, while
//     core.HandleAllRequest binds URI before JSON, so a body field would
//     overwrite the identity the URL names;
//   - request gains values from neither the path nor the body (claims, a route
//     flag).
//
// They all run before validation, so a validator sees the same identity the
// store will act on.
package restbind

import (
	"errors"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/errno"
)

// ErrUnexpectedRequestType reports a binder that was handed a request of a
// different type. It is a programming error, never a caller error.
var ErrUnexpectedRequestType = errors.New("restbind: unexpected request type")

// JSONWithPath binds a JSON body, then lets the caller copy path parameters
// into the request. The copy runs before validation, so a rule that requires a
// name sees the path value rather than an empty body field.
func JSONWithPath[T any](c *gin.Context, mutate func(*T)) core.Binder {
	return func(obj any) error {
		rq, ok := obj.(*T)
		if !ok {
			return errno.ErrInternal.WithMessage(
				"restbind: unexpected request type: received %T, want %T", obj, (*T)(nil))
		}

		if err := c.ShouldBindJSON(rq); err != nil {
			return err
		}

		mutate(rq)

		return nil
	}
}

// JSONWithPathAllowEmpty is JSONWithPath for an endpoint whose body is
// optional: an empty body is a valid request, not a bind error.
func JSONWithPathAllowEmpty[T any](c *gin.Context, mutate func(*T)) core.Binder {
	return func(obj any) error {
		rq, ok := obj.(*T)
		if !ok {
			return errno.ErrInternal.WithMessage(
				"restbind: unexpected request type: received %T, want %T", obj, (*T)(nil))
		}

		if err := c.ShouldBindJSON(rq); err != nil && !errors.Is(err, io.EOF) {
			return err
		}

		mutate(rq)

		return nil
	}
}

// URIWith binds the path parameters into the request, then lets the caller set
// values that come from neither the path nor the body (claims, a route flag).
// The generated messages declare their path fields with `@gotags: uri:"..."`;
// the copy runs before validation, so a rule sees the value the store will use.
func URIWith[T any](c *gin.Context, mutate func(*T)) core.Binder {
	return func(obj any) error {
		rq, ok := obj.(*T)
		if !ok {
			return errno.ErrInternal.WithMessage(
				"restbind: unexpected request type: received %T, want %T", obj, (*T)(nil))
		}

		if err := c.ShouldBindUri(rq); err != nil {
			return err
		}

		mutate(rq)

		return nil
	}
}

// URIAndQuery binds path parameters and query parameters into one request.
func URIAndQuery[T any](c *gin.Context) core.Binder {
	return func(obj any) error {
		rq, ok := obj.(*T)
		if !ok {
			return errno.ErrInternal.WithMessage(
				"restbind: unexpected request type: received %T, want %T", obj, (*T)(nil))
		}

		if err := c.ShouldBindUri(rq); err != nil {
			return err
		}

		return c.ShouldBindQuery(rq)
	}
}
