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

package chatmodel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/pkg/options"
)

// TestOpenAICompatibleSendsConfiguredHeaders pins the gateway-routing escape
// hatch: some OpenAI-compatible gateways (OpenCode, for one) require a header
// the OpenAI wire format does not define, and refuse every request without it.
func TestOpenAICompatibleSendsConfiguredHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	m, err := NewOpenAICompatibleModel(options.ChatModelConfig{
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "test-model",
		Headers: map[string]string{
			"x-opencode-session": "onexai",
			// A configured header must not be able to replace the credential the
			// client authenticated with.
			"Authorization": "Bearer wrong",
		},
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatibleModel: %v", err)
	}
	if _, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if got.Get("x-opencode-session") != "onexai" {
		t.Fatalf("x-opencode-session = %q, want onexai", got.Get("x-opencode-session"))
	}
	if auth := got.Get("Authorization"); auth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", auth)
	}
}

// TestInheritCopiesHeaders pins that an auxiliary model which inherits its
// connection from the deployment also inherits the gateway headers: a vision or
// reviewer call to the same gateway faces the same admission rule.
func TestInheritCopiesHeaders(t *testing.T) {
	base := options.ChatModelConfig{Headers: map[string]string{"x-opencode-session": "onexai"}}
	got := Inherit(options.ChatModelConfig{}, base)
	if got.Headers["x-opencode-session"] != "onexai" {
		t.Fatalf("inherited headers = %v, want x-opencode-session=onexai", got.Headers)
	}
}
