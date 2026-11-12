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

package webhook

import (
	"errors"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/agent/model"
)

// valid returns a hook that passes every rule; the cases below vary one field
// from it, so a failure names the rule that broke rather than a missing field.
func valid() *model.WebhookM {
	return &model.WebhookM{
		Name:   "onex-quota",
		Status: model.StatusEnabled,
		URL:    "https://onex.example.com/internal/v1/agent-hook",
		Events: model.StringList{model.EventOnStart, model.EventOnEnd},
	}
}

func TestValidateAcceptsAWorkingConfiguration(t *testing.T) {
	w := valid()
	if err := Validate(w); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if w.TimeoutSeconds != defaultTimeoutSeconds {
		t.Errorf("timeout = %d, want the default %d", w.TimeoutSeconds, defaultTimeoutSeconds)
	}
}

func TestValidateRejects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mut   func(*model.WebhookM)
		field string
	}{
		{"empty name", func(w *model.WebhookM) { w.Name = "  " }, "name"},
		{"empty url", func(w *model.WebhookM) { w.URL = "" }, "url"},
		{
			// url.Parse reads "onex.internal:8188" as the scheme "onex.internal",
			// so without this rule the failure would be a dial error at call time.
			"url with no scheme", func(w *model.WebhookM) { w.URL = "onex.internal:8188/hook" }, "url",
		},
		{"unsupported scheme", func(w *model.WebhookM) { w.URL = "ftp://onex.internal/hook" }, "url"},
		{"no events", func(w *model.WebhookM) { w.Events = nil }, "events"},
		{"unknown event", func(w *model.WebhookM) { w.Events = model.StringList{"onFinish"} }, "events"},
		{"unknown failure mode", func(w *model.WebhookM) { w.FailureMode = "explode" }, "failureMode"},
		{
			// A hook that only reports has no run left to refuse, so the setting
			// would be stored and silently do nothing.
			"fail mode without onStart",
			func(w *model.WebhookM) {
				w.Events = model.StringList{model.EventOnEnd}
				w.FailureMode = model.FailureModeFail
			},
			"failureMode",
		},
		{"negative timeout", func(w *model.WebhookM) { w.TimeoutSeconds = -1 }, "timeoutSeconds"},
		{"timeout above the ceiling", func(w *model.WebhookM) { w.TimeoutSeconds = timeoutMaxSeconds + 1 }, "timeoutSeconds"},
		{
			// Overriding X-Onexai-Event would change which event the receiver
			// believes it is answering.
			"reserved header",
			func(w *model.WebhookM) { w.Headers = model.StringMap{"X-Onexai-Event": "onEnd"} },
			"headers",
		},
		{"reserved header, lower case", func(w *model.WebhookM) { w.Headers = model.StringMap{"x-onexai-token": "x"} }, "headers"},
		{"unknown status", func(w *model.WebhookM) { w.Status = "paused" }, "status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := valid()
			tc.mut(w)
			err := Validate(w)
			if err == nil {
				t.Fatalf("Validate() = nil, want a refusal naming %q", tc.field)
			}
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Validate() = %T, want *ValidationError", err)
			}
			if verr.Field != tc.field {
				t.Errorf("refused %q, want %q (%v)", verr.Field, tc.field, err)
			}
		})
	}
}

// TestValidateAcceptsFailureModeFailWithOnStart is the other half of the rule
// above: the mode is refused only where it cannot take effect.
func TestValidateAcceptsFailureModeFailWithOnStart(t *testing.T) {
	w := valid()
	w.FailureMode = model.FailureModeFail
	if err := Validate(w); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TestValidateAcceptsCustomHeaders covers headers that are not reserved: the
// rule is about the x-onexai- namespace, not about headers in general.
func TestValidateAcceptsCustomHeaders(t *testing.T) {
	w := valid()
	w.Headers = model.StringMap{"X-Tenant": "onex", "X-Route": "p0"}
	if err := Validate(w); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TestValidationErrorNamesTheField pins the message shape the 400 renders: an
// operator reading "events: ..." knows which field to change.
func TestValidationErrorNamesTheField(t *testing.T) {
	err := Validate(&model.WebhookM{Name: "x", URL: "https://h"})
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Validate() = %v, want *ValidationError", err)
	}
	if !strings.HasPrefix(verr.Error(), "events: ") {
		t.Errorf("Error() = %q, want it to start with the field name", verr.Error())
	}
}
