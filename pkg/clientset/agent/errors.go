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

package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"resty.dev/v3"
)

// maxErrorBodyBytes bounds how much of a failed response's body is kept. The
// bodies this client reads are onexstack's error envelopes, which are small;
// the limit exists so a proxy that answers with something huge cannot make the
// client allocate it.
const maxErrorBodyBytes = 8 << 10

// HTTPError describes a non-2xx response from oai-agent-apiserver. It carries the
// status and the server's own explanation instead of collapsing both into a
// status-only string, because the caller has to tell apart failures that look
// identical once the body is gone: a 401 says this server's credentials are
// wrong, a 403 says the person asking may not use this agent, and a 5xx says
// something broke. Only the status is load-bearing — Reason and Message exist
// for logs, never for matching on prose.
type HTTPError struct {
	// StatusCode is the HTTP status of the refused response.
	StatusCode int
	// Reason is the envelope's machine-readable reason, e.g. "PermissionDenied"
	// or "Unauthenticated". Empty when the body was not an envelope.
	Reason string
	// Message is the envelope's human-readable explanation. It is the server's
	// text, often English and sometimes naming an internal subject id, so it
	// belongs in logs rather than on a user-facing card.
	Message string
	// Body is the trimmed raw response when it was not a decodable envelope,
	// kept for diagnosis and bounded by maxErrorBodyBytes.
	Body string
}

// Error renders the refusal for logs. It deliberately includes Message when
// there is one: whoever reads the log next needs the server's own sentence,
// which is exactly what a status-only error used to throw away.
func (e *HTTPError) Error() string {
	switch {
	case e.Reason != "" && e.Message != "":
		return fmt.Sprintf("agent returned %d (%s): %s", e.StatusCode, e.Reason, e.Message)
	case e.Message != "":
		return fmt.Sprintf("agent returned %d: %s", e.StatusCode, e.Message)
	case e.Body != "":
		return fmt.Sprintf("agent returned %d: %s", e.StatusCode, e.Body)
	default:
		return fmt.Sprintf("agent returned %d", e.StatusCode)
	}
}

// IsUnauthenticated reports whether err is a refusal of this server's
// credentials (HTTP 401). The fault is ours — a missing or wrong AK/SK, or a
// signature that did not survive the trip — not the asking user's.
func IsUnauthenticated(err error) bool {
	return statusCodeOf(err) == http.StatusUnauthorized
}

// IsForbidden reports whether err is an authorization refusal (HTTP 403): the
// credentials were accepted, and the subject named in the request may not use
// what it asked for.
func IsForbidden(err error) bool {
	return statusCodeOf(err) == http.StatusForbidden
}

// IsTooManyRequests reports whether err is a refusal with time on it (HTTP
// 429): the credentials were accepted and the request was understood, but an
// allowance for this account is spent. Retrying later is what the status means.
func IsTooManyRequests(err error) bool {
	return statusCodeOf(err) == http.StatusTooManyRequests
}

// IsNotFound reports whether err is the service saying the named run (or the
// session behind it) does not exist (HTTP 404). For a job that polls a run by
// id this is terminal news, not a transient failure: the run will not come
// back, so the caller has to settle the job instead of polling forever.
func IsNotFound(err error) bool {
	return statusCodeOf(err) == http.StatusNotFound
}

// IsPermissionError reports whether err is one of the two refusals a person can
// be told about: 401 and 403. It is the single entry point for that judgement
// so no caller has to hardcode the pair.
func IsPermissionError(err error) bool {
	code := statusCodeOf(err)
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

// HTTPErrorMessage returns the server's explanation carried by err, or "" when
// err is not an HTTPError. Message is preferred over the raw body; both are
// meant for logs.
func HTTPErrorMessage(err error) string {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return ""
	}
	if httpErr.Message != "" {
		return httpErr.Message
	}
	return httpErr.Body
}

// statusCodeOf returns the HTTP status carried by err, or 0 when it carries
// none.
func statusCodeOf(err error) int {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return 0
	}
	return httpErr.StatusCode
}

// newHTTPError builds an HTTPError from a refused response and closes its body.
//
// Closing happens here rather than at each call site because there are several
// refusal branches and every one of them used to return without closing: a
// streamed request is opened with SetDoNotParseResponse, so nothing downstream
// consumes the body, and a connection left unread leaks. Reading it here is
// what makes the close meaningful — the bytes are the reason the caller needs
// the error to carry.
func newHTTPError(resp *resty.Response) *HTTPError {
	if resp == nil {
		return &HTTPError{}
	}
	httpErr := &HTTPError{StatusCode: resp.StatusCode()}

	// A parsed (non-streamed) response has already been read by resty and is
	// reachable through Bytes; a streamed one has not, and only the raw body
	// holds the bytes. Bytes first, so both shapes work.
	raw := resp.Bytes()
	if len(raw) == 0 && resp.RawResponse != nil && resp.RawResponse.Body != nil {
		body := resp.RawResponse.Body
		defer body.Close()
		raw, _ = io.ReadAll(io.LimitReader(body, maxErrorBodyBytes))
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return httpErr
	}

	var envelope struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && (envelope.Reason != "" || envelope.Message != "") {
		httpErr.Reason, httpErr.Message = envelope.Reason, envelope.Message
		return httpErr
	}
	// Not an envelope: a proxy's HTML, a plain-text rejection, an unexpected
	// shape. Keep it as-is so the log says what actually came back.
	httpErr.Body = string(raw)
	return httpErr
}
