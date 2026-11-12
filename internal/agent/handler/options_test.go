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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Authorization disabled means no registered backend; the response is still a
// well-formed empty list, and "none" stays a UI-only sentinel.
func TestListAuthzBackendsWithoutCoordinatorReturnsEmptyList(t *testing.T) {
	c, recorder := newRecorder(t)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/authz/backends", nil)

	(&Handler{}).ListAuthzBackends(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var body struct {
		Backends []string `json:"backends"`
		Count    int      `json:"count"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Backends) != 0 || body.Count != 0 {
		t.Fatalf("got backends=%v count=%d, want an empty list", body.Backends, body.Count)
	}
}
