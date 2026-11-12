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

package lark

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"
)

func TestIsBotMention(t *testing.T) {
	tests := []struct {
		name string
		m    *larkim.MentionEvent
		want bool
	}{
		{
			name: "nil mention",
			m:    nil,
			want: false,
		},
		{
			name: "bot by mentioned_type",
			m:    &larkim.MentionEvent{MentionedType: ptr.To("bot")},
			want: true,
		},
		{
			name: "user by mentioned_type",
			m:    &larkim.MentionEvent{MentionedType: ptr.To("user")},
			want: false,
		},
		{
			name: "fallback: bot has no tenant user_id",
			m:    &larkim.MentionEvent{Id: &larkim.UserId{OpenId: ptr.To("ou_bot")}},
			want: true,
		},
		{
			name: "fallback: user carries user_id",
			m:    &larkim.MentionEvent{Id: &larkim.UserId{UserId: ptr.To("ou_user"), OpenId: ptr.To("ou_user")}},
			want: false,
		},
		{
			name: "nil id treated as bot",
			m:    &larkim.MentionEvent{},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsBotMention(tt.m); got != tt.want {
				t.Errorf("IsBotMention() = %v, want %v", got, tt.want)
			}
		})
	}
}
