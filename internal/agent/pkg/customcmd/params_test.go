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

package customcmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/agent/model"
)

func TestValidateParamsAppliesDefaultsAndTypes(t *testing.T) {
	params := []model.CommandParameterM{
		{Name: "env", Type: "string", Default: json.RawMessage(`"dev"`)},
		{Name: "count", Type: "integer"},
		{Name: "verbose", Type: "boolean"},
		{Name: "ratio", Type: "number"},
		{Name: "wait", Type: "duration"},
		{Name: "tags", Type: "array"},
		{Name: "extra", Type: "json"},
	}

	out, err := ValidateParams(params, map[string]any{
		"count":   float64(3),
		"verbose": true,
		"ratio":   float64(0.5),
		"wait":    "30s",
		"tags":    []any{"a", "b"},
		"extra":   map[string]any{"k": "v"},
	})
	if err != nil {
		t.Fatalf("ValidateParams: %v", err)
	}
	if out["env"] != "dev" {
		t.Errorf("default env = %v, want dev", out["env"])
	}
	if out["count"] != int64(3) {
		t.Errorf("count = %v (%T), want int64(3)", out["count"], out["count"])
	}
	if out["verbose"] != true {
		t.Errorf("verbose = %v", out["verbose"])
	}

	// An omitted optional parameter is present as its zero value, so a template
	// can test it without tripping missingkey=error.
	zeroed, err := ValidateParams([]model.CommandParameterM{{Name: "flag", Type: "boolean"}}, map[string]any{})
	if err != nil {
		t.Fatalf("ValidateParams: %v", err)
	}
	if value, ok := zeroed["flag"]; !ok || value != false {
		t.Errorf("omitted boolean = %v (present %v), want false", value, ok)
	}
}

func TestValidateParamsRejectsBadInput(t *testing.T) {
	scalar := func(name, ptype string) model.CommandParameterM {
		return model.CommandParameterM{Name: name, Type: ptype}
	}

	tests := []struct {
		name   string
		params []model.CommandParameterM
		input  map[string]any
		want   string
	}{
		{
			name:   "missing required",
			params: []model.CommandParameterM{{Name: "env", Type: "string", Required: true}},
			input:  map[string]any{},
			want:   "required",
		},
		{
			name:   "unknown key",
			params: []model.CommandParameterM{scalar("env", "string")},
			input:  map[string]any{"evn": "prod"},
			want:   "unknown parameter",
		},
		{
			name:   "wrong type",
			params: []model.CommandParameterM{scalar("count", "integer")},
			input:  map[string]any{"count": "three"},
			want:   "must be of type integer",
		},
		{
			name:   "enum",
			params: []model.CommandParameterM{{Name: "env", Type: "string", Enum: []string{"dev", "prod"}}},
			input:  map[string]any{"env": "stage"},
			want:   "must be one of",
		},
		{
			name:   "bad duration",
			params: []model.CommandParameterM{scalar("wait", "duration")},
			input:  map[string]any{"wait": "soon"},
			want:   "duration",
		},
		{
			name: "schema required field",
			params: []model.CommandParameterM{{
				Name:   "obj",
				Type:   "object",
				Schema: json.RawMessage(`{"type":"object","required":["name"]}`),
			}},
			input: map[string]any{"obj": map[string]any{}},
			want:  "required field",
		},
		{
			name: "schema type",
			params: []model.CommandParameterM{{
				Name:   "obj",
				Type:   "object",
				Schema: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`),
			}},
			input: map[string]any{"obj": map[string]any{"n": "x"}},
			want:  "type integer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateParams(tt.params, tt.input)
			if err == nil {
				t.Fatalf("ValidateParams accepted %v", tt.input)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}
