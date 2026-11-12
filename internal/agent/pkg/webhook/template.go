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
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/webhookclient"
)

// maxRenderedBody bounds what a template may produce. A hook's template is
// written by whoever administers the hooks, so a mistake in it must not become
// an arbitrarily large request to the endpoint or an arbitrarily large read
// back.
const maxRenderedBody = 64 << 10

// renderBody produces the request body for one delivery: the hook's template
// rendered if it has one, otherwise the default payload.
//
// # What a template may name
//
// The variables are the fields of the default payload, spelled the way the
// payload spells them on the wire — so `{{.subject.id}}` and
// `{{.usage.calls}}` reach the same values the default body carries under those
// names. A hook uses a template when it wants its own names for them
// (`{"who": "{{.subject.id}}"}`), which is the whole point: the receiver
// declares the mapping and nothing here has to guess what it wants.
//
// # Why the body is not marshalled from the rendered text
//
// The text is sent as written. A template that renders something which is not
// JSON is the hook author's mistake and the endpoint's 400 — wrapping it in a
// validity check here would mean parsing a payload this service does not
// interpret, to reject a request the receiver is about to reject anyway.
func renderBody(hook *model.WebhookM, p webhookclient.Payload) ([]byte, error) {
	if strings.TrimSpace(hook.Template) == "" {
		return json.Marshal(p)
	}
	tpl, err := template.New(hook.Name).Option("missingkey=zero").Parse(hook.Template)
	if err != nil {
		return nil, fmt.Errorf("webhook %q: template does not parse: %w", hook.Name, err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, templateVars(p)); err != nil {
		return nil, fmt.Errorf("webhook %q: template did not render: %w", hook.Name, err)
	}
	if buf.Len() > maxRenderedBody {
		return nil, fmt.Errorf("webhook %q: rendered body is %d bytes, over the %d limit",
			hook.Name, buf.Len(), maxRenderedBody)
	}
	return buf.Bytes(), nil
}

// templateVars exposes the payload under the names a template uses. The nested
// maps are what make `{{.subject.id}}` resolve: text/template reads a
// map[string]any by key, so the dotted form works without the fields being
// spelled in Go's exported style.
func templateVars(p webhookclient.Payload) map[string]any {
	vars := map[string]any{
		"objectKind": p.ObjectKind,
		"event":      p.Event,
		"agent":      p.Agent,
		"agents":     p.Agents,
		"surface":    p.Surface,
		"sessionKey": p.SessionKey,
		"runID":      p.RunID,
		"subject": map[string]any{
			"kind": p.Subject.Kind,
			"id":   p.Subject.ID,
			"name": p.Subject.Name,
		},
	}
	if p.Usage != nil {
		vars["usage"] = map[string]any{
			"promptTokens":     p.Usage.PromptTokens,
			"completionTokens": p.Usage.CompletionTokens,
			"calls":            p.Usage.Calls,
			"cacheHitTokens":   p.Usage.CacheHitTokens,
			"cacheMissTokens":  p.Usage.CacheMissTokens,
		}
	}
	return vars
}
