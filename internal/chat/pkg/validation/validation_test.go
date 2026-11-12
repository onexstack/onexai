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

package validation

import (
	"context"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
)

// TestValidateOncallGroupRequiresName pins the rule the switch turns on. The
// store keys on-call groups by name, so a blank one would produce a row nobody
// can address.
func TestValidateOncallGroupRequiresName(t *testing.T) {
	v := New(nil)

	tests := []struct {
		name    string
		group   *model.OncallGroupM
		wantErr bool
	}{
		{name: "named group", group: &model.OncallGroupM{Name: "sre"}},
		{name: "blank name", group: &model.OncallGroupM{Name: "   "}, wantErr: true},
		{name: "empty object", group: &model.OncallGroupM{}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateOncallGroup(context.Background(), tt.group)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("ValidateOncallGroup() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "name is required") {
				t.Errorf("error %q does not say why", err)
			}
		})
	}
}

// TestValidateOncallGroupChecksAgentReferences pins that the agents column is
// validated as a reference list at write time.
//
// The failure it prevents is late and quiet: these entries are handed to the
// scheduler's expand, which errors on a malformed entry or an unknown
// reference kind. By then the group is live and its ticket groups are answering
// every question with an error card, with nothing linking that back to the typo
// in this column.
func TestValidateOncallGroupChecksAgentReferences(t *testing.T) {
	v := New(nil)

	tests := []struct {
		name     string
		agents   string
		wantErr  bool
		errWords string
	}{
		{name: "no agents at all", agents: ""},
		{name: "empty array", agents: "[]"},
		{name: "bare agent name", agents: `["ops-agent"]`},
		{name: "reference", agents: `["@peer"]`},
		{name: "reference with arg", agents: `["@oncall:ops"]`},
		{name: "mixed", agents: `["@peer","ops-agent","@default"]`},

		{name: "unknown kind", agents: `["@bogus"]`, wantErr: true, errWords: "no known reference kind"},
		{name: "sigil with nothing after it", agents: `["@"]`, wantErr: true, errWords: "malformed"},
		{name: "blank entry", agents: `["@peer",""]`, wantErr: true, errWords: "entry 2"},
		{name: "not JSON", agents: "ops-agent", wantErr: true, errWords: "not a JSON array"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group := &model.OncallGroupM{Name: "sre", Agents: tt.agents}
			err := v.ValidateOncallGroup(context.Background(), group)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("ValidateOncallGroup() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), tt.errWords) {
				t.Errorf("error %q does not mention %q", err, tt.errWords)
			}
		})
	}
}
