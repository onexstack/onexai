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
	"encoding/json"
	"regexp"
	"strings"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
)

// ExtractMessageText converts a Lark message body (JSON) of the given msg_type
// into human-readable display text. Shared by the history and live-event paths
// so both render text consistently and never leak raw JSON.
func ExtractMessageText(msgType, content string) string {
	switch msgType {
	case "text":
		var tc struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(content), &tc) == nil && tc.Text != "" {
			return tc.Text
		}
		return content
	case "post":
		if s := extractPostText(content); s != "" {
			return s
		}
		return formatKnownOrPlaceholder(content, "[图文]")
	case "interactive":
		if s := ExtractCardText(content); s != "" {
			return s
		}
		return "[卡片]"
	case "system":
		return parseSystemMessage(content)
	case "image":
		return "[图片]"
	case "audio":
		return "[语音]"
	case "media":
		return "[视频]"
	case "sticker":
		return "[表情]"
	case "file":
		return formatKnownOrPlaceholder(content, "[文件]")
	case "share_chat":
		return "[群名片]"
	case "share_user":
		return "[个人名片]"
	default:
		return formatKnownOrPlaceholder(content, "["+msgType+"]")
	}
}

// ExtractImageKey parses the image_key out of a Lark image message body
// ({"image_key":"img_xxx"}). Returns "" when absent or malformed.
func ExtractImageKey(content string) string {
	if content == "" {
		return ""
	}
	var m struct {
		ImageKey string `json:"image_key"`
	}
	if json.Unmarshal([]byte(content), &m) == nil && m.ImageKey != "" {
		return m.ImageKey
	}
	return ""
}

// extractPostText extracts plain text from a Lark "post" (rich-text) message.
// Format: {"zh_cn":{"title":"...","content":[[{"tag":"text","text":"..."},...],...]}}
func extractPostText(content string) string {
	if content == "" {
		return ""
	}

	// Bare (non-language-wrapped) shape emitted by some clients:
	// {"title":"","content":[[{"tag":"text","text":"..."}]], "content_v2":[...]}.
	// Decode this directly so we don't fall through to the regex fallback (which
	// would also scrape the duplicate content_v2 block).
	var bare struct {
		Title   string              `json:"title"`
		Content [][]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &bare); err == nil && (bare.Title != "" || len(bare.Content) > 0) {
		if s := joinPostLines(bare.Title, bare.Content); s != "" {
			return s
		}
	}

	// Try structured parse first.
	var outer map[string]struct {
		Title   string              `json:"title"`
		Content [][]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &outer); err == nil {
		var parts []string
		for _, lang := range outer {
			if lang.Title != "" {
				parts = append(parts, lang.Title)
			}
			for _, line := range lang.Content {
				for _, nodeRaw := range line {
					var node struct {
						Tag  string `json:"tag"`
						Text string `json:"text"`
						Href string `json:"href"`
					}
					if err := json.Unmarshal(nodeRaw, &node); err == nil && node.Text != "" {
						parts = append(parts, node.Text)
					}
				}
			}
			break // only process the first language block
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	// Fallback: regex extraction of "text":"..." pairs.
	re := regexp.MustCompile(`"text"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	matches := re.FindAllStringSubmatch(content, -1)
	var parts []string
	for _, m := range matches {
		if len(m) >= 2 && m[1] != "" {
			parts = append(parts, m[1])
		}
	}
	return strings.Join(parts, " ")
}

// joinPostLines turns a Lark post's title + content matrix (lines of text nodes)
// into plain text: nodes within a line are concatenated, lines joined by "\n".
func joinPostLines(title string, content [][]json.RawMessage) string {
	var lines []string
	if title != "" {
		lines = append(lines, title)
	}
	for _, line := range content {
		var seg strings.Builder
		for _, nodeRaw := range line {
			var node struct {
				Tag  string `json:"tag"`
				Text string `json:"text"`
			}
			if json.Unmarshal(nodeRaw, &node) == nil && node.Text != "" {
				seg.WriteString(node.Text)
			}
		}
		lines = append(lines, seg.String())
	}
	return strings.Join(lines, "\n")
}

// formatKnownOrPlaceholder recognises a few common non-text bodies (file, vote)
// and formats them; otherwise it returns the placeholder (never the raw JSON).
func formatKnownOrPlaceholder(content, placeholder string) string {
	if content == "" {
		return placeholder
	}
	// File: {"file_key":"...","file_name":"..."}.
	var f struct {
		FileName string `json:"file_name"`
	}
	if json.Unmarshal([]byte(content), &f) == nil && f.FileName != "" {
		return "[文件] " + f.FileName
	}
	// Vote / topic: {"topic":"...","options":["a","b"]}.
	var v struct {
		Topic   string   `json:"topic"`
		Options []string `json:"options"`
	}
	if json.Unmarshal([]byte(content), &v) == nil && v.Topic != "" {
		if len(v.Options) > 0 {
			return "[投票] " + v.Topic + "：" + strings.Join(v.Options, " / ")
		}
		return "[投票] " + v.Topic
	}
	return placeholder
}

// ExtractCardText extracts the readable text of a Lark interactive card, or ""
// when the card has none to give.
//
// The reading itself belongs to larkcard, which owns every card shape in this
// repo; this is the door onto it for the surfaces that want a card as a single
// string — the cached history and the TUI mirror. Only a card whose content was
// in the body answers: a remote template and a cardkit entity carry an id and
// nothing else, so they answer "" and the caller omits them rather than printing
// a placeholder into somebody's history.
func ExtractCardText(content string) string {
	card, source, err := larkcard.ParseCard(content)
	if err != nil || source != larkcard.CardSourceInline {
		return ""
	}
	return strings.TrimSpace(card.RenderText())
}

// parseSystemMessage formats a Lark system message from its template JSON.
// Example content: {"template":"{from_user} invited {to_chatters} to the chat.",
// "from_user":["Alice"],"to_chatters":["Bob","Carol"],"divider_text":{}}
func parseSystemMessage(content string) string {
	if content == "" {
		return "[系统消息]"
	}
	var message struct {
		Template   string   `json:"template"`
		FromUser   []string `json:"from_user"`
		ToChatters []string `json:"to_chatters"`
	}
	if err := json.Unmarshal([]byte(content), &message); err != nil || message.Template == "" {
		return "[系统消息]"
	}
	text := message.Template
	if len(message.FromUser) > 0 {
		text = strings.ReplaceAll(text, "{from_user}", strings.Join(message.FromUser, ", "))
	}
	if len(message.ToChatters) > 0 {
		text = strings.ReplaceAll(text, "{to_chatters}", strings.Join(message.ToChatters, ", "))
	}
	return text
}
