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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// capturingAgentBiz records the request the handler built and then refuses it, so
// nothing downstream runs.
//
// Refusing is what keeps the test to one boundary: a nil event channel would make
// the handler stream from nothing and hang, and there is no answer to assert on
// here — the question is what the handler *understood*, not what it answered.
type capturingAgentBiz struct {
	agentapi.IAgentBiz
	got *agentapi.AskRequest
}

func (c *capturingAgentBiz) Ask(_ context.Context, req *agentapi.AskRequest) (<-chan agentapi.Event, error) {
	c.got = req
	return nil, errors.New("stop here")
}

// TestReportUsageReachesTheBusinessLayerFromTheWire is the boundary that let a 2x
// charge live for months: the platform had been sending `reportUsage: false` and
// nothing here read it.
//
// It has to be checked end to end from the JSON because every step of it is
// silent when it breaks. A field renamed in the proto, a handler that forgot to
// copy it, or a tri-state collapsed to a plain bool all produce the same thing —
// a run that answers 200, streams an answer, and is recorded twice — with no
// error anywhere to notice.
func TestReportUsageReachesTheBusinessLayerFromTheWire(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantMetering agentapi.Metering
		wantReport   bool
	}{
		{
			name:         "the platform says it meters this run itself",
			body:         `{"sessionId":"s","input":{"text":"q"},"subject":{"id":"user-1"},"metering":{"mode":"MODE_CALLER"}}`,
			wantMetering: agentapi.MeteringCaller,
			wantReport:   false,
		},
		{
			name:         "a caller that never mentions the field keeps the old behaviour",
			body:         `{"sessionId":"s","input":{"text":"q"},"subject":{"id":"user-1"}}`,
			wantMetering: agentapi.MeteringUnspecified,
			wantReport:   true,
		},
		{
			name:         "a caller that explicitly asks to be reported",
			body:         `{"sessionId":"s","input":{"text":"q"},"subject":{"id":"user-1"},"metering":{"mode":"MODE_RUNTIME"}}`,
			wantMetering: agentapi.MeteringRuntime,
			wantReport:   true,
		},
		{
			name:         "a platform-owned run is not metered at all",
			body:         `{"sessionId":"s","input":{"text":"q"},"subject":{"id":"user-1"},"metering":{"mode":"MODE_NONE"}}`,
			wantMetering: agentapi.MeteringNone,
			wantReport:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			captured := &capturingAgentBiz{}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/agent/ask", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")

			NewHandler(Deps{Biz: &fakeBiz{agent: captured}}).AskAgent(c)

			if captured.got == nil {
				t.Fatalf("the handler never reached the business layer (status %d): %s", w.Code, w.Body.String())
			}
			if captured.got.Metering != tc.wantMetering {
				t.Fatalf("Metering = %v, want %v", captured.got.Metering, tc.wantMetering)
			}
			if got := agentapi.ReportsUsage(captured.got); got != tc.wantReport {
				t.Fatalf("ReportsUsage = %v, want %v", got, tc.wantReport)
			}
		})
	}
}
