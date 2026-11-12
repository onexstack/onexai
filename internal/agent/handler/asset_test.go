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

package handler

import (
	"net/http"
	"testing"

	"github.com/onexstack/onexstack/pkg/errorsx"

	"github.com/onexstack/onexai/internal/agent/biz/v1/asset"
	"github.com/onexstack/onexai/pkg/media"
)

// TestAssetErrnoTellsNotConfiguredFromAFailure pins the distinction F3 exists
// for: "this deployment has no media store" must be readable from the wire,
// not only from the message. Collapsing it into ErrInternal would force every
// consumer to match message text to tell it from a store that failed.
func TestAssetErrnoTellsNotConfiguredFromAFailure(t *testing.T) {
	// The biz's sentinel is media's, so either name identifies the condition.
	for _, err := range []error{asset.ErrNotConfigured, media.ErrNotConfigured} {
		got := assetErrno(err)
		if code := errorsx.Code(got); code != http.StatusServiceUnavailable {
			t.Errorf("assetErrno(%v) code = %d, want %d", err, code, http.StatusServiceUnavailable)
		}
		if reason := errorsx.Reason(got); reason == "" {
			t.Errorf("assetErrno(%v) has no reason, so the condition is only in the message", err)
		}
	}

	// A store that failed is still an internal error, and must not be dressed
	// up as "not configured".
	other := assetErrno(errorsx.ErrInternal)
	if code := errorsx.Code(other); code != http.StatusInternalServerError {
		t.Errorf("assetErrno(a store failure) code = %d, want %d", code, http.StatusInternalServerError)
	}
}
