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

package bot

import (
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestParseCreateTicketForm(t *testing.T) {
	action := &callback.CallBackAction{
		FormValue: map[string]interface{}{
			"selected_module": "eks",
			"selected_title":  "Pod 一直 Pending",
			"selected_level":  "2",
		},
		Value: map[string]interface{}{"chat_type": "p2p"},
	}

	form, err := parseCreateTicketForm("create_ticket", action)
	if err != nil {
		t.Fatalf("parseCreateTicketForm() error = %v", err)
	}
	if form.Module != "eks" || form.Title != "Pod 一直 Pending" || form.Level != 2 || form.ChatType != "p2p" {
		t.Errorf("parsed form = %+v", form)
	}
}

func TestParseCreateTicketFormDefaultsLevel(t *testing.T) {
	action := &callback.CallBackAction{
		FormValue: map[string]interface{}{
			"selected_module": "eks",
			"selected_title":  "x",
		},
	}
	form, err := parseCreateTicketForm("create_ticket", action)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if form.Level != 4 {
		t.Errorf("Level = %d, want the default 4", form.Level)
	}
}

func TestParseCreateTicketFormModuleFromActionValue(t *testing.T) {
	action := &callback.CallBackAction{FormValue: map[string]interface{}{"selected_title": "x"}}
	form, err := parseCreateTicketForm("create_ticket:obs", action)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if form.Module != "obs" {
		t.Errorf("Module = %q, want obs", form.Module)
	}
}

func TestParseCreateTicketFormRejectsMissingFields(t *testing.T) {
	cases := map[string]*callback.CallBackAction{
		"no module": {FormValue: map[string]interface{}{"selected_title": "x"}},
		"no title":  {FormValue: map[string]interface{}{"selected_module": "eks"}},
		"nil":       nil,
	}
	for name, action := range cases {
		if _, err := parseCreateTicketForm("create_ticket", action); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
