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

package resource

import (
	"os"
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/prometheus"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// TestPodLivePrometheus runs the real command against a live Prometheus. It is
// skipped unless OAI_POD_LIVE_ENDPOINT names one.
func TestPodLivePrometheus(t *testing.T) {
	endpoint := os.Getenv("OAI_POD_LIVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("set OAI_POD_LIVE_ENDPOINT to run the live check")
	}

	client := prometheus.NewClient(endpoint)
	if !client.Configured() {
		t.Fatalf("client not configured for %q", endpoint)
	}

	sr := &runtime.StaticRuntime{Clientset: &stubClientset{prometheus: client}}
	err, res := runPod(t, sr, "oncall-celery", "-n", "observability")
	if err != nil {
		t.Fatalf("ExecuteContext() = %v", err)
	}
	t.Logf("raw:\n%s", res.Raw)
	t.Logf("markdown:\n%s", res.Markdown.Content)
	t.Logf("plain:\n%s", res.PlainText)
}
