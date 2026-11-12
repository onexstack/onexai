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

package controlplanestore

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/chat/model"
)

// allAgentsSentinel is how the legacy row shape says "every agent": the row's
// agent list predates selectors, so the meaning lives in a value. The binding
// projection uses the same spelling so the resolver sees one vocabulary.
const allAgentsSentinel = "*"

func statusOf(enabled bool) string {
	if enabled {
		return ""
	}
	return model.AgentCommandStatusDisabled
}

func enabledOf(status string) bool { return status != model.AgentCommandStatusDisabled }

func unixMilli(t metav1.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func jsonList(values []string) string { return model.EncodeNameList(values) }

func decodeList(raw string) []string { return model.DecodeNameList(raw) }

func selectorString(selector *metav1.LabelSelector) string {
	parsed, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return ""
	}
	return parsed.String()
}
