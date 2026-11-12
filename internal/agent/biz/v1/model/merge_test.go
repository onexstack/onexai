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

package model

import (
	"testing"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
)

func TestMergeModelUpdateKeepsStoredFields(t *testing.T) {
	existing := &agentmodel.ModelM{
		APIKey:       "sealed",
		Owner:        "u1",
		Capabilities: []string{"vision"},
		Provider:     "deepseek",
		Model:        "deepseek-chat",
		BaseURL:      "https://api.deepseek.com/v1",
		CreatedAt:    42,
	}
	row := &agentmodel.ModelM{Name: "m"}

	mergeModelUpdate(row, existing)

	if row.APIKey != "sealed" || row.Owner != "u1" ||
		len(row.Capabilities) != 1 || row.Capabilities[0] != "vision" ||
		row.Provider != "deepseek" || row.Model != "deepseek-chat" ||
		row.BaseURL != "https://api.deepseek.com/v1" || row.CreatedAt != 42 {
		t.Fatalf("merge did not preserve stored fields: %+v", row)
	}
}

func TestMergeModelUpdateKeepsRequestValues(t *testing.T) {
	existing := &agentmodel.ModelM{
		APIKey:       "sealed",
		Owner:        "u1",
		Capabilities: []string{"vision"},
		Provider:     "deepseek",
		Model:        "deepseek-chat",
		BaseURL:      "https://api.deepseek.com/v1",
		CreatedAt:    42,
	}
	row := &agentmodel.ModelM{
		Name:         "m",
		APIKey:       "new",
		Owner:        "u2",
		Capabilities: []string{"audio"},
		Provider:     "qwen",
		Model:        "qwen-max",
		BaseURL:      "https://dashscope.aliyuncs.com/compatible-mode/v1",
		CreatedAt:    7,
	}

	mergeModelUpdate(row, existing)

	if row.APIKey != "new" || row.Owner != "u2" ||
		len(row.Capabilities) != 1 || row.Capabilities[0] != "audio" ||
		row.Provider != "qwen" || row.Model != "qwen-max" ||
		row.BaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" || row.CreatedAt != 7 {
		t.Fatalf("merge overwrote request values: %+v", row)
	}
}

func TestResolveBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		row     agentmodel.ModelM
		want    string
		wantErr bool
	}{
		{
			name: "an explicit endpoint wins",
			row:  agentmodel.ModelM{Provider: "deepseek", BaseURL: "https://gateway.internal/v1"},
			want: "https://gateway.internal/v1",
		},
		{
			name: "the provider's default fills an empty endpoint",
			row:  agentmodel.ModelM{Provider: "deepseek"},
			want: "https://api.deepseek.com/v1",
		},
		{
			name: "the model name infers the provider when it is unset",
			row:  agentmodel.ModelM{Model: "deepseek-chat"},
			want: "https://api.deepseek.com/v1",
		},
		{
			name:    "a provider without a default needs an explicit endpoint",
			row:     agentmodel.ModelM{Provider: "ollama"},
			wantErr: true,
		},
		{
			name:    "a custom provider without an endpoint is rejected",
			row:     agentmodel.ModelM{Provider: "self-hosted", Model: "x"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := tc.row
			err := resolveBaseURL(&row)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveBaseURL() = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBaseURL() error = %v", err)
			}
			if row.BaseURL != tc.want {
				t.Fatalf("BaseURL = %q, want %q", row.BaseURL, tc.want)
			}
		})
	}
}
