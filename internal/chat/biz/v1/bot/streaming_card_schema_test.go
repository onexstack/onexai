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
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// TestStreamingCardJSONPassesCard2Schema runs the one card that does not go through
// larkcard's renderer through larkcard's card 2.0 property guard. The streaming card
// is hand-built here — panel, answer, stop row and all — because cardkit needs the
// element_ids as literals, so nothing in the typed builder covers its shape.
//
// Feishu rejects a whole card over a single unknown property (200621), and this card
// is created before the turn has produced anything: a rejected creation is a turn
// with no progress, no reasoning and no stop button, and the failure surfaces only
// as a warning in the log.
func TestStreamingCardJSONPassesCard2Schema(t *testing.T) {
	cases := map[string]struct {
		title         string
		initialText   string
		showReasoning bool
		stop          stopButton
		print         cardkitPrint
	}{
		"answer only": {},
		"with reasoning panel": {
			title:         "正在处理",
			showReasoning: true,
			stop:          stopButton{RunID: "run-1", SessionKey: "oc_chat", ChatType: v1.ChatTypeGroup},
		},
		"stop button without a conversation": {
			stop: stopButton{RunID: "run-1"},
		},
		"configured print hints": {
			title:       "正在处理",
			initialText: "开始",
			stop:        stopButton{RunID: "run-1", SessionKey: "ou_user", ChatType: v1.ChatTypeP2P},
			print:       cardkitPrint{FrequencyMs: 80, Step: 5, Strategy: "delay"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cardJSON, err := buildStreamingCardJSON(tc.title, tc.initialText, tc.showReasoning, tc.stop, tc.print)
			if err != nil {
				t.Fatalf("buildStreamingCardJSON returned error: %v", err)
			}
			if violations := larkcard.Card2Violations(cardJSON); len(violations) > 0 {
				t.Fatalf("streaming card carries properties Feishu rejects:\n  %s",
					strings.Join(violations, "\n  "))
			}
		})
	}
}
