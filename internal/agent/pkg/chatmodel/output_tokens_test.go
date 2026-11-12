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
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestOutputCeilingPrecedence pins the resolution order, because the three
// sources have different owners: the deployment sets llm.max-tokens, the handler
// resolves a per-surface value onto the context, and a call site that knows
// better passes model.WithMaxTokens. A silent reordering would let the
// deployment's default win over a caller that asked for something else — or let
// a group chat's answer ceiling clip an internal call that never asked for one.
func TestOutputCeilingPrecedence(t *testing.T) {
	intPtr := func(n int) *int { return &n }
	eighty := func() *OpenAIChatModel { return (&OpenAIChatModel{}).WithMaxTokens(8192) }

	tests := []struct {
		name  string
		model *OpenAIChatModel
		ctx   context.Context
		opts  *model.Options
		want  int
	}{
		{
			name:  "the deployment's setting is the fallback",
			model: eighty(),
			ctx:   context.Background(),
			opts:  &model.Options{},
			want:  8192,
		},
		{
			name:  "a per-surface ceiling on the context beats it",
			model: eighty(),
			ctx:   WithMaxOutputTokens(context.Background(), 2048),
			opts:  &model.Options{},
			want:  2048,
		},
		{
			name:  "a per-call option beats both",
			model: eighty(),
			ctx:   WithMaxOutputTokens(context.Background(), 2048),
			opts:  &model.Options{MaxTokens: intPtr(4096)},
			want:  4096,
		},
		{
			// The summarizer's opt-out: it clears the per-surface value so that a
			// group chat's 2048 cannot truncate the session archive, and the
			// deployment's own setting then applies.
			name:  "clearing the per-surface value falls back to the deployment's",
			model: eighty(),
			ctx:   WithMaxOutputTokens(WithMaxOutputTokens(context.Background(), 2048), 0),
			opts:  &model.Options{},
			want:  8192,
		},
		{
			name:  "a model with no setting of its own has no ceiling",
			model: &OpenAIChatModel{},
			ctx:   context.Background(),
			opts:  &model.Options{},
			want:  0,
		},
		{
			name:  "an empty context has no ceiling",
			model: eighty(),
			ctx:   context.Background(),
			opts:  nil,
			want:  8192,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.model.outputCeiling(tc.ctx, tc.opts); got != tc.want {
				t.Fatalf("outputCeiling = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestBuildRequestMaxTokensIsOmittedWhenUnset pins the wire shape: 0 must mean
// "no field", not a literal zero. `"max_tokens": 0` is a request for a
// completion of no tokens, so sending it would break every run rather than leave
// the ceiling to the model.
func TestBuildRequestMaxTokensIsOmittedWhenUnset(t *testing.T) {
	m := &OpenAIChatModel{}

	unset, err := m.buildRequest([]*schema.Message{schema.UserMessage("hi")}, false, "test-model", nil, 0)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	raw, err := json.Marshal(unset)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "max_tokens") {
		t.Fatalf("an unset ceiling must not be sent, got %s", raw)
	}

	capped, err := m.buildRequest([]*schema.Message{schema.UserMessage("hi")}, false, "test-model", nil, 2048)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if capped.MaxTokens != 2048 {
		t.Fatalf("MaxTokens = %d, want 2048", capped.MaxTokens)
	}
}
