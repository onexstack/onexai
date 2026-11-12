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

package apiserver

import (
	"net/http"
	"strings"
)

// DeprecationRule maps one served path prefix to the warning a client sees.
//
// It is deliberately a path-prefix rule rather than a REST-storage hook: the
// strategy files under internal/apiserver are generated from the declserver
// scaffold and must not be hand-edited. The HTTP chain is the one place that
// can add a warning without touching them.
type DeprecationRule struct {
	// PathPrefix is matched against the request path, including /status and
	// /watch subresources.
	PathPrefix string
	// Warning is the message after the RFC 7234 `299 - "..."` marker.
	Warning string
}

// DeprecatedResources are the declarative APIs that no runtime reads or writes
// any more. They stay served only so an existing object can be inspected or
// migrated; the warning tells a caller before it builds a dependency on them.
var DeprecatedResources = []DeprecationRule{
	{
		PathPrefix: "/apis/chat.onexai.io/v1alpha1/dutygroups",
		Warning:    "DutyGroup is deprecated; duty groups are a chat-owned SQL resource",
	},
	{
		PathPrefix: "/apis/chat.onexai.io/v1alpha1/botapps",
		Warning:    "BotApp is deprecated; chat reads bot configuration from its service config",
	},
	{
		PathPrefix: "/apis/agent.onexai.io/v1alpha1/scheduledruns",
		Warning:    "ScheduledRun objects are deprecated; use batch.onex.ai CronJob (the /v1/agent/scheduledruns facade remains supported)",
	},
}

// WrapDeprecationWarnings adds the standard `Warning` response header to a
// request whose path belongs to a deprecated resource.
func WrapDeprecationWarnings(next http.Handler, rules []DeprecationRule) http.Handler {
	if next == nil || len(rules) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, rule := range rules {
			if strings.HasPrefix(r.URL.Path, rule.PathPrefix) {
				w.Header().Add("Warning", `299 - "`+rule.Warning+`"`)
				break
			}
		}
		next.ServeHTTP(w, r)
	})
}
