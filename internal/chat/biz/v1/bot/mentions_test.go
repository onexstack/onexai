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

import "testing"

// TestHasMentionCandidate pins what counts as an "@" worth a members lookup.
//
// The distinction is load-bearing rather than cosmetic: a positive answer costs
// a Lark API round trip inside the reply path, and command help text writes "@"
// as prose — /assign's summary is "将当前工单指派给 @ 的人员处理" (assign the ticket to the @-mentioned people) — so every
// /help used to wait on a members call it could not possibly need.
func TestHasMentionCandidate(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "no marker", text: "你好", want: false},
		{name: "prose marker", text: "将当前工单指派给 @ 的人员处理", want: false},
		{name: "trailing marker", text: "问一下 @", want: false},
		{name: "tab after marker", text: "@\t张三", want: false},
		{name: "name after marker", text: "请 @张三 看一下", want: true},
		{name: "latin name", text: "@go.kong please take a look", want: true},
		{name: "one prose and one real", text: "@ 的值是 @张三 的工号", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasMentionCandidate(tt.text); got != tt.want {
				t.Fatalf("hasMentionCandidate(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}
