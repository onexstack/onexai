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

package chatname

import (
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
)

func TestParseAndFormatRoundTrip(t *testing.T) {
	raw := FormatName("处理中", 3, "Pod 一直 Pending")
	parts, ok := Parse(raw)
	if !ok {
		t.Fatalf("Parse(%q) reported an invalid name", raw)
	}
	if parts.Status != "处理中" || parts.Level != "P3" || parts.Title != "Pod 一直 Pending" {
		t.Errorf("Parse(%q) = %+v", raw, parts)
	}
	if got := FormatName(parts.Status, 3, parts.Title); got != raw {
		t.Errorf("round trip = %q, want %q", got, raw)
	}
}

func TestParseRejectsForeignChatNames(t *testing.T) {
	for _, raw := range []string{"", "普通群", "[处理中]没有级别", "[]"} {
		if _, ok := Parse(raw); ok {
			t.Errorf("Parse(%q) should reject a non-ticket name", raw)
		}
	}
}

func TestLevelStringClampsOutOfRange(t *testing.T) {
	cases := map[int64]string{1: "P1", 2: "P2", 3: "P3", 4: "P4", 0: "P4", 5: "P4"}
	for in, want := range cases {
		if got := LevelString(in); got != want {
			t.Errorf("LevelString(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestStatusForTicket(t *testing.T) {
	cases := map[model.TicketStatus]string{
		model.TicketStatusOpen:       "处理中",
		model.TicketStatusProcessing: "处理中",
		model.TicketStatusSaved:      "已保存",
		model.TicketStatusClosed:     "已关闭",
		model.TicketStatusResolved:   "已关闭",
	}
	for in, want := range cases {
		if got := StatusForTicket(in); got != want {
			t.Errorf("StatusForTicket(%q) = %q, want %q", in, got, want)
		}
	}
}
