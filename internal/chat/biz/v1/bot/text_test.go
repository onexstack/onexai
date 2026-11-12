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
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

func TestParseMsgContentFallback(t *testing.T) {
	tests := []struct {
		name       string
		msgType    string
		rawContent string
		wantText   string
	}{
		{"file with name", "file", `{"file_key":"fk","file_name":"report.pdf"}`, "[文件] report.pdf"},
		{"file without name", "file", `{"file_key":"fk"}`, "[文件]"},
		{"audio", "audio", `{"file_key":"fk"}`, "[语音]"},
		{"media", "media", `{"file_key":"fk"}`, "[视频]"},
		{"sticker", "sticker", `{"file_key":"fk"}`, "[表情]"},
		{"share_chat", "share_chat", `{}`, "[群名片]"},
		{"unknown type", "weird", `{"x":"y"}`, "[weird]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := parseMsgContent(tt.msgType, tt.rawContent)
			if mc == nil {
				t.Fatalf("parseMsgContent(%q) = nil, want text %q", tt.msgType, tt.wantText)
			}
			if mc.Text != tt.wantText {
				t.Errorf("parseMsgContent(%q).Text = %q, want %q", tt.msgType, mc.Text, tt.wantText)
			}
		})
	}
}

func TestParseMsgContentUnsupportedEmpty(t *testing.T) {
	// A type with no parser and empty extractable text must not leak raw JSON.
	if got := parseMsgContent("file", `{"file_key":"fk"}`); got == nil {
		t.Fatal("parseMsgContent(file with no name) = nil, want a placeholder")
	}
}

// botMention is the mention shape a group message carries when the user
// @-mentions the bot: no user id (that is what makes it a bot), a placeholder
// key pointing at the text, and the display name.
func botMention(key, name string) runtime.MentionedUser {
	return runtime.MentionedUser{Key: key, Name: name, IsBot: true}
}

// TestNormalizeQuestion pins the one transformation that decides what the model
// is asked. The rows are the shapes the two entry points actually produce: a P2P
// body with no mention at all, a group body the handler already resolved, and a
// group body that still carries the platform's placeholder because the mention
// had no display name to substitute.
func TestNormalizeQuestion(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		mentions []runtime.MentionedUser
		want     string
	}{
		{
			name: "p2p has no mention to remove",
			text: "请构建onex pipeline中的content服务",
			want: "请构建onex pipeline中的content服务",
		},
		{
			name: "group, handler already resolved the placeholder",
			text: "@Helper 请构建onex pipeline中的content服务",
			mentions: []runtime.MentionedUser{
				botMention("@_user_1", "Helper"),
			},
			want: "请构建onex pipeline中的content服务",
		},
		{
			name: "group, placeholder still in the body",
			text: "@_user_1 请构建onex pipeline中的content服务",
			mentions: []runtime.MentionedUser{
				botMention("@_user_1", "Helper"),
			},
			want: "请构建onex pipeline中的content服务",
		},
		{
			// The reason the trim runs last: strip an already-resolved mention and
			// the space in front of the sentence is what is left behind.
			name: "group, no display name so the placeholder survived",
			text: "@_user_1 请构建onex pipeline中的content服务",
			mentions: []runtime.MentionedUser{
				{Key: "@_user_1", IsBot: true},
			},
			want: "请构建onex pipeline中的content服务",
		},
		{
			// A human whose display name equals the bot's. Keyed on IsBot, not on a
			// name comparison, so this one is kept and attributed.
			name: "a human with the bot's name is not the bot",
			text: "@_user_1 请构建onex pipeline中的content服务",
			mentions: []runtime.MentionedUser{
				{Key: "@_user_1", Name: "Helper", OpenID: "ou_colleague"},
			},
			want: "@Helper 请构建onex pipeline中的content服务",
		},
		{
			// The bot's Lark display name need not equal the configured one; the
			// mention still has to go.
			name: "bot display name differs from the configured name",
			text: "@OneX助手 请构建",
			mentions: []runtime.MentionedUser{
				botMention("@_user_1", "OneX助手"),
			},
			want: "请构建",
		},
		{
			name: "both a person and the bot are mentioned",
			text: "@_user_1 请和 @_user_2 一起构建",
			mentions: []runtime.MentionedUser{
				botMention("@_user_1", "Helper"),
				{Key: "@_user_2", Name: "张三", OpenID: "ou_zhang"},
			},
			want: "请和 @张三 一起构建",
		},
		{
			// Lark numbers the placeholders in mention order, which is not
			// necessarily text order; the mapping follows the slice, so the
			// placeholder must be read from the mention, not rebuilt from the index
			// when the mention carries one.
			name: "mentions are not in text order",
			text: "@_user_2 请和 @_user_1 一起",
			mentions: []runtime.MentionedUser{
				botMention("@_user_1", "Helper"),
				{Key: "@_user_2", Name: "张三", OpenID: "ou_zhang"},
			},
			want: "@张三 请和 一起",
		},
		{
			name: "mention in the middle of a sentence",
			text: "麻烦 @_user_1 构建 content",
			mentions: []runtime.MentionedUser{
				botMention("@_user_1", "Helper"),
			},
			want: "麻烦 构建 content",
		},
		{
			// The command rung sits below this, so a leading mention must not be
			// what stands between the command and the command tree.
			name: "group command",
			text: "@_user_1 /pipeline content -e edu-onex",
			mentions: []runtime.MentionedUser{
				botMention("@_user_1", "Helper"),
			},
			want: "/pipeline content -e edu-onex",
		},
		{
			name:     "a bot mention with nothing said",
			text:     "@_user_1",
			mentions: []runtime.MentionedUser{botMention("@_user_1", "Helper")},
			want:     "",
		},
		{
			name: "an unnamed non-bot mention does not become a bare @",
			text: "@_user_1 请看这个",
			mentions: []runtime.MentionedUser{
				{Key: "@_user_1", OpenID: "ou_unknown"},
			},
			want: "请看这个",
		},
		{
			// Feishu decorates bare links; a command hidden behind one would miss
			// the prefix test the same way an unremoved mention would.
			name: "auto-linked text is not a mention",
			text: "[https://example.com](https://example.com) 你好",
			want: "[https://example.com](https://example.com) 你好",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeQuestion(tt.text, tt.mentions); got != tt.want {
				t.Errorf("normalizeQuestion(%q, %d mentions) = %q, want %q",
					tt.text, len(tt.mentions), got, tt.want)
			}
		})
	}
}

// TestNormalizeQuestionNameAndPlaceholderBothGo covers the one case where the
// body contains the mention twice, in both spellings — which happens when a
// message is edited, or when a client re-sends a body it already resolved.
func TestNormalizeQuestionNameAndPlaceholderBothGo(t *testing.T) {
	got := normalizeQuestion("@_user_1 @Helper 构建",
		[]runtime.MentionedUser{botMention("@_user_1", "Helper")})
	if got != "构建" {
		t.Errorf("normalizeQuestion() = %q, want %q", got, "构建")
	}
}

// TestGroupAndP2PAskTheSameQuestion is the claim the whole exercise is about:
// the same sentence reaches the model identically whether it was sent in a
// private conversation or in a group with the bot @-mentioned. Every row below
// is a body the two entry points really produce, and all of them have to come
// out the same.
//
// The assertion stops at the text because the ladder and the turn have separate
// fixtures by design: walk builds its runtime from the event and hands the
// outbound side back to the biz layer, so a unit test that ran a handoff all
// the way into a turn would be driving the platform path with a fake client
// rather than measuring the transformation. What the rungs do with the result
// is pinned next door, by TestGroupCommandSurvivesTheMention and
// TestMentionedGroupMessageIsAnswered; the two rungs consume one variable, so a
// difference between them can no longer be introduced without deleting the
// variable.
func TestGroupAndP2PAskTheSameQuestion(t *testing.T) {
	const question = "请构建onex pipeline中的content服务"

	// The private conversation carries no mention at all.
	p2p := normalizeQuestion(question, nil)
	if p2p != question {
		t.Fatalf("a private message was altered: %q, want %q", p2p, question)
	}

	group := map[string]struct {
		text     string
		mentions []runtime.MentionedUser
	}{
		"handler resolved the placeholder": {
			text:     "@Helper " + question,
			mentions: []runtime.MentionedUser{botMention("@_user_1", "Helper")},
		},
		"placeholder still in the body": {
			text:     "@_user_1 " + question,
			mentions: []runtime.MentionedUser{botMention("@_user_1", "Helper")},
		},
		"mention has no display name": {
			text:     "@_user_1 " + question,
			mentions: []runtime.MentionedUser{{Key: "@_user_1", IsBot: true}},
		},
		"bot display name differs from the configured name": {
			text:     "@OneX助手 " + question,
			mentions: []runtime.MentionedUser{botMention("@_user_1", "OneX助手")},
		},
	}

	for name, tt := range group {
		t.Run(name, func(t *testing.T) {
			got := normalizeQuestion(tt.text, tt.mentions)
			if got != p2p {
				t.Errorf("the group question is %q, the private one is %q; the model would be asked two different things",
					got, p2p)
			}
		})
	}
}
