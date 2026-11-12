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

// Package webhook holds the run-lifecycle hooks: the rows that configure them
// (see model.WebhookM), the administration API over them, and the dispatcher
// that calls them before and after a run.
package webhook

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/onexstack/onexai/internal/agent/model"
)

// nameMaxLen mirrors the column size on model.WebhookM.Name. A longer value
// would be truncated or rejected by the driver rather than by the API.
const nameMaxLen = 128

// timeoutMaxSeconds bounds a hook's own timeout. onStart sits on a run's
// critical path, and a ceiling the server enforces is what keeps one hook's
// misconfiguration from turning into every run's delay.
const timeoutMaxSeconds = 30

// defaultTimeoutSeconds is what a hook with no stated timeout gets. Three
// seconds is long enough for a call on a LAN and short enough that a dead
// endpoint costing every run three seconds is noticed.
const defaultTimeoutSeconds = 3

// headerPrefix is the namespace this service's delivery headers use. A hook's
// own headers may not use it: overriding X-Onexai-Event would change which
// event the receiver thinks it is answering.
const headerPrefix = "x-onexai-"

// authTokenMaxLen bounds a pasted credential. It is generous enough for a JWT
// and still refuses a body used as an accidental credential dump.
const authTokenMaxLen = 8192

// ValidationError reports one rejected field. It carries the field name so the
// handler can name it in the 400 — "events must name at least one of ..." is
// actionable, "invalid request" is not.
type ValidationError struct {
	Field   string
	Message string
}

// Error implements error.
func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Message) }

// Validate reports the first problem with a hook's configuration, or nil.
func Validate(w *model.WebhookM) error {
	if w == nil {
		return &ValidationError{Field: "webhook", Message: "is required"}
	}
	if err := validateName(w.Name); err != nil {
		return err
	}
	if err := validateURL(w.URL); err != nil {
		return err
	}
	if err := validateAuthToken(w); err != nil {
		return err
	}
	if err := validateEvents(w); err != nil {
		return err
	}
	if err := validateFailureMode(w); err != nil {
		return err
	}
	if err := validateTimeout(w); err != nil {
		return err
	}
	if err := validateHeaders(w.Headers); err != nil {
		return err
	}
	return validateStatus(w.Status)
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return &ValidationError{Field: "name", Message: "must not be empty"}
	}
	if len(name) > nameMaxLen {
		return &ValidationError{Field: "name", Message: fmt.Sprintf("must be at most %d characters", nameMaxLen)}
	}
	return nil
}

// validateURL refuses anything the caller cannot be told apart from an
// unreachable endpoint at call time. A bare host:port is the case worth
// naming: url.Parse reads "onex.internal:8188" as the scheme "onex.internal",
// so the failure would be a dial error that looks like a network problem.
func validateURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return &ValidationError{Field: "url", Message: "must not be empty"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return &ValidationError{Field: "url", Message: fmt.Sprintf("is not a URL: %v", err)}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &ValidationError{Field: "url", Message: fmt.Sprintf("must start with http:// or https://, got %q", u.Scheme)}
	}
	if u.Host == "" {
		return &ValidationError{Field: "url", Message: "must name a host"}
	}
	return nil
}

// validateAuthToken keeps the two credential forms apart. A plaintext token is
// moved into the secret store before the row is written; accepting it beside a
// reference would make "which one wins" a property of the write path rather
// than of the object.
func validateAuthToken(w *model.WebhookM) error {
	token := strings.TrimSpace(w.AuthToken)
	if token == "" {
		return nil
	}
	if w.SecretRef != "" {
		return &ValidationError{
			Field:   "authToken",
			Message: "must not be set together with secretRef",
		}
	}
	if len(token) > authTokenMaxLen {
		return &ValidationError{
			Field:   "authToken",
			Message: fmt.Sprintf("must be at most %d characters", authTokenMaxLen),
		}
	}
	return nil
}

// validateEvents refuses a hook that subscribes to nothing, because it would be
// stored, listed and never called — the kind of row an operator reads as "this
// is configured" while nothing is.
func validateEvents(w *model.WebhookM) error {
	if len(w.Events) == 0 {
		return &ValidationError{
			Field:   "events",
			Message: fmt.Sprintf("must name at least one of %q, %q", model.EventOnStart, model.EventOnEnd),
		}
	}
	for _, e := range w.Events {
		if e != model.EventOnStart && e != model.EventOnEnd {
			return &ValidationError{
				Field:   "events",
				Message: fmt.Sprintf("has unknown event %q (want %q or %q)", e, model.EventOnStart, model.EventOnEnd),
			}
		}
	}
	return nil
}

func validateFailureMode(w *model.WebhookM) error {
	switch w.FailureMode {
	case "", model.FailureModeIgnore, model.FailureModeFail:
	default:
		return &ValidationError{
			Field:   "failureMode",
			Message: fmt.Sprintf("must be %q or %q, got %q", model.FailureModeIgnore, model.FailureModeFail, w.FailureMode),
		}
	}
	// A hook that only reports cannot refuse the run it reports on: there is no
	// run left to refuse. Refusing the row is better than storing a setting
	// whose whole meaning is that it does nothing.
	if w.FailureMode == model.FailureModeFail && !w.Subscribes(model.EventOnStart) {
		return &ValidationError{
			Field:   "failureMode",
			Message: fmt.Sprintf("%q has no effect on a hook that does not subscribe to %q", model.FailureModeFail, model.EventOnStart),
		}
	}
	return nil
}

func validateTimeout(w *model.WebhookM) error {
	if w.TimeoutSeconds == 0 {
		w.TimeoutSeconds = defaultTimeoutSeconds
		return nil
	}
	if w.TimeoutSeconds < 0 {
		return &ValidationError{Field: "timeoutSeconds", Message: "must not be negative"}
	}
	if w.TimeoutSeconds > timeoutMaxSeconds {
		return &ValidationError{
			Field:   "timeoutSeconds",
			Message: fmt.Sprintf("must be at most %d", timeoutMaxSeconds),
		}
	}
	return nil
}

// validateHeaders refuses a custom header that would shadow a delivery header.
// The delivery headers are how the receiver knows which event it is answering
// and which hook sent it; a hook that could overwrite them could make itself
// indistinguishable from another.
func validateHeaders(headers map[string]string) error {
	for k := range headers {
		if strings.HasPrefix(strings.ToLower(k), headerPrefix) {
			return &ValidationError{
				Field:   "headers",
				Message: fmt.Sprintf("must not set %q: the %s* headers are reserved for delivery", k, headerPrefix),
			}
		}
	}
	return nil
}

func validateStatus(status string) error {
	switch status {
	case "", model.StatusEnabled, model.StatusDisabled:
		return nil
	default:
		return &ValidationError{
			Field:   "status",
			Message: fmt.Sprintf("must be %q or %q, got %q", model.StatusEnabled, model.StatusDisabled, status),
		}
	}
}
