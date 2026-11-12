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

package apiserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWrapDeprecationWarningsAddsTheHeader(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := WrapDeprecationWarnings(next, []DeprecationRule{{
		PathPrefix: "/apis/chat.onexai.io/v1alpha1/dutygroups",
		Warning:    "DutyGroup is deprecated",
	}})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/apis/chat.onexai.io/v1alpha1/dutygroups/team-a", nil))

	got := recorder.Header().Get("Warning")
	if !strings.Contains(got, `299 - "DutyGroup is deprecated"`) {
		t.Fatalf("Warning = %q", got)
	}
}

func TestWrapDeprecationWarningsLeavesOtherPathsAlone(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := WrapDeprecationWarnings(next, DeprecatedResources)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/apis/agent.onexai.io/v1alpha1/agents", nil))

	if got := recorder.Header().Get("Warning"); got != "" {
		t.Fatalf("unexpected Warning header: %q", got)
	}
}
