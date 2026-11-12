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

// Package storeerr turns a by-key store read into the error an API caller
// expects.
package storeerr

import (
	"errors"

	"gorm.io/gorm"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/pkg/declclient/declstore"
)

// GetError answers a by-key Get that produced no row.
//
// The control-plane reader reports an absent row as (nil, nil) rather than as
// an error — see declstore.Reader.Get, whose callers have to tell "absent" from
// "unreadable" — so a caller that inspects only err answers 200 with an empty
// envelope. A service-to-service caller reads that as "the upstream does not
// serve this route", not as "not found", and its create-if-missing flow then
// never creates the object: that is how a product's knowledge base stopped being
// registered once the agent store moved to the control plane.
//
// Everything else is mapped through Read: a database outage must not read as a
// 404, and it must not reach the handler as a raw gorm/client-go error either
// (that would serialize the backend's text to the caller).
func GetError[T any](row *T, err error, notFoundFormat string, args ...any) error {
	if err == nil && row == nil {
		err = gorm.ErrRecordNotFound
	}
	if errors.Is(err, gorm.ErrRecordNotFound) || apierrors.IsNotFound(err) {
		return errno.ErrNotFound.WithMessage(notFoundFormat, args...)
	}
	if errors.Is(err, declstore.ErrAmbiguousKey) {
		return ambiguous(err)
	}
	return Read(err)
}

// ambiguous turns a key that matched several namespaces into a 400. Picking
// one would hand the caller another tenant's object, so the refusal names the
// action instead.
func ambiguous(err error) error {
	var keyErr *declstore.AmbiguousKeyError
	if errors.As(err, &keyErr) {
		return errno.ErrInvalidArgument.WithMessage(
			"%q 在多个命名空间中存在，请使用 namespace/name 指定", keyErr.Key)
	}
	return errno.ErrInvalidArgument.WithMessage("名称在多个命名空间中存在，请使用 namespace/name 指定")
}

// Read maps a store read failure to the API's read error while preserving the
// cause, so a caller can still test it with errors.Is and an operator can see
// what failed.
func Read(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, declstore.ErrAmbiguousKey) {
		return ambiguous(err)
	}
	return errno.ErrDBRead.Wrap(err)
}

// Write maps a store write failure to the API's write error while preserving
// the cause.
//
// A control-plane store reports its classification as a Kubernetes StatusError.
// Flattening every one of those into ErrDBWrite turned "already exists" or
// "forbidden" into a 500 and hid the only part a caller can act on, so those
// are mapped to the API taxonomy first; only errors with no control-plane
// classification (a real SQL/driver/transport failure) stay DBWrite.
func Write(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, declstore.ErrAmbiguousKey):
		return ambiguous(err)
	case apierrors.IsAlreadyExists(err):
		return errno.ErrAlreadyExists.Wrap(err)
	case apierrors.IsNotFound(err):
		return errno.ErrNotFound.Wrap(err)
	case apierrors.IsInvalid(err):
		return errno.ErrInvalidArgument.Wrap(err)
	case apierrors.IsForbidden(err):
		return errno.ErrPermissionDenied.Wrap(err)
	case apierrors.IsUnauthorized(err):
		return errno.ErrUnauthenticated.Wrap(err)
	}

	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		return errno.ErrInternal.WithMessage(
			"the control plane rejected the write: %s", statusErr.ErrStatus.Message,
		).Wrap(err)
	}

	return errno.ErrDBWrite.Wrap(err)
}
