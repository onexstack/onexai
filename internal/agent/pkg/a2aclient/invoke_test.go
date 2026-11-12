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

package a2aclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInvokeV03(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method != "message/send" {
			t.Errorf("method = %q", request.Method)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"kind":"task","id":"task-1","contextId":"ctx-1",
			"status":{"state":"completed"},
			"artifacts":[{"artifactId":"a1","parts":[{"kind":"text","text":"hello"}]}]
		}}`))
	}))
	defer server.Close()

	invoker := NewInvoker(server.URL, ProtocolVersion03, TransportJSONRPC, server.Client(), nil)
	result, err := invoker.Send(context.Background(), Request{Message: "hi"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.State != StateCompleted || result.TaskID != "task-1" || result.Text != "hello" {
		t.Fatalf("result = %+v", result)
	}
}

func TestInvokeV10(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method != "SendMessage" {
			t.Errorf("method = %q", request.Method)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"task":{"id":"task-2","contextId":"ctx-2",
			"status":{"state":"TASK_STATE_COMPLETED"},
			"artifacts":[{"artifactId":"a2","parts":[{"text":"world"}]}]}
		}}`))
	}))
	defer server.Close()

	invoker := NewInvoker(server.URL, ProtocolVersion10, TransportJSONRPC, server.Client(), nil)
	result, err := invoker.Send(context.Background(), Request{Message: "hi"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.State != StateCompleted || result.TaskID != "task-2" || result.Text != "world" {
		t.Fatalf("result = %+v", result)
	}
}

func TestStreamV03(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","taskId":"task-3","contextId":"ctx-3","status":{"state":"working"},"final":false}}

`))
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"artifact-update","taskId":"task-3","artifact":{"artifactId":"a3","parts":[{"kind":"text","text":"chunk"}]}}}

`))
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","taskId":"task-3","contextId":"ctx-3","status":{"state":"completed"},"final":true}}

`))
	}))
	defer server.Close()

	invoker := NewInvoker(server.URL, ProtocolVersion03, TransportJSONRPC, server.Client(), nil)
	events, err := invoker.Stream(context.Background(), Request{Message: "hi"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var sawArtifact, sawFinal bool
	for event := range events {
		switch event.Type {
		case "artifact":
			sawArtifact = true
		case "status":
			if event.Final && event.State == StateCompleted {
				sawFinal = true
			}
		}
	}
	if !sawArtifact || !sawFinal {
		t.Fatalf("artifact=%v final=%v", sawArtifact, sawFinal)
	}
}
