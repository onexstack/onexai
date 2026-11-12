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

package observe

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// histogramCount returns how many observations the named histogram has across
// all its series, or 0 when it has never been observed. The gathered family
// type is used without naming it, so the test does not need a direct
// client_model dependency.
func histogramCount(t *testing.T, reg *prometheus.Registry, name string) uint64 {
	t.Helper()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		var total uint64
		for _, m := range f.GetMetric() {
			total += m.GetHistogram().GetSampleCount()
		}
		return total
	}
	return 0
}

func newObserver(t *testing.T) (*modelObserver, *prometheus.Registry) {
	t.Helper()

	reg := prometheus.NewRegistry()
	m := metrics.New(reg, true)
	if m == nil {
		t.Fatal("metrics.New returned nil")
	}
	return &modelObserver{m: m}, reg
}

func TestModelCallbackRecordsOneCall(t *testing.T) {
	o, reg := newObserver(t)
	ctx := agentapi.WithModelName(context.Background(), "deepseek")

	ctx = o.OnStart(ctx, nil, []*schema.Message{{Role: schema.User, Content: "hi"}})
	if stateFrom(ctx) == nil {
		t.Fatal("OnStart did not record a call for model input")
	}
	o.OnEnd(ctx, nil, &schema.Message{Role: schema.Assistant, Content: "hello"})

	if got := histogramCount(t, reg, "onexai_agent_model_duration_seconds"); got != 1 {
		t.Errorf("model call observations = %d, want 1", got)
	}
}

func TestModelCallbackIgnoresNonModelComponents(t *testing.T) {
	o, reg := newObserver(t)
	ctx := context.Background()

	ctx = o.OnStart(ctx, nil, "not a model input")
	if stateFrom(ctx) != nil {
		t.Fatal("OnStart recorded a call for a non-model input")
	}
	o.OnEnd(ctx, nil, "tool output")

	if got := histogramCount(t, reg, "onexai_agent_model_duration_seconds"); got != 0 {
		t.Errorf("model call observations = %d, want 0", got)
	}
}

// TestModelCallbackCountsAnErrorOnce guards the done flag: the agent above the
// model also reports errors, and an agent failure after a successful model call
// must not be counted as a second, failed model call.
func TestModelCallbackCountsAnErrorOnce(t *testing.T) {
	o, reg := newObserver(t)
	ctx := agentapi.WithModelName(context.Background(), "deepseek")

	ctx = o.OnStart(ctx, nil, []*schema.Message{{Role: schema.User, Content: "hi"}})
	o.OnEnd(ctx, nil, &schema.Message{Role: schema.Assistant, Content: "hello"})
	// The agent fails after the model succeeded.
	o.OnError(ctx, nil, errors.New("agent failed"))

	if got := histogramCount(t, reg, "onexai_agent_model_duration_seconds"); got != 1 {
		t.Errorf("model call observations = %d, want 1", got)
	}
}

func TestModelCallbackStreamFirstToken(t *testing.T) {
	o, reg := newObserver(t)
	ctx := agentapi.WithModelName(context.Background(), "deepseek")

	ctx = o.OnStart(ctx, nil, []*schema.Message{{Role: schema.User, Content: "hi"}})

	stream, w := schema.Pipe[callbacks.CallbackOutput](4)
	_ = w.Send("chunk-1", nil)
	_ = w.Send("chunk-2", nil)
	w.Close()

	o.OnEndWithStreamOutput(ctx, nil, stream)

	// The handler consumes its copy in a goroutine; wait for it to finish.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if histogramCount(t, reg, "onexai_agent_model_first_token_seconds") == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first-token observation never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := histogramCount(t, reg, "onexai_agent_model_duration_seconds"); got != 1 {
		t.Errorf("model call observations = %d, want 1", got)
	}
}
