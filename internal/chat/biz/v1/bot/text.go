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
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"

	imlark "github.com/onexstack/onexai/internal/chat/im/lark"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// extractPostText extracts plain text from a Lark "post" (rich-text) message.
// Format: {"zh_cn":{"title":"...","content":[[{"tag":"text","text":"..."},...],...]}}
func extractPostText(content string) string {
	if content == "" {
		return ""
	}

	// Bare (non-language-wrapped) shape emitted by some clients.
	var bare struct {
		Title   string              `json:"title"`
		Content [][]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &bare); err == nil && (bare.Title != "" || len(bare.Content) > 0) {
		if s := joinPostLines(bare.Title, bare.Content); s != "" {
			return s
		}
	}

	// Structured parse first.
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

// joinPostLines turns a Lark post's title + content matrix into plain text.
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
	var f struct {
		FileName string `json:"file_name"`
	}
	if json.Unmarshal([]byte(content), &f) == nil && f.FileName != "" {
		return "[文件] " + f.FileName
	}
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

// parseSystemMessage formats a Lark system message from its template JSON.
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

// MsgContent holds the parsed content of a Lark message event.
type MsgContent struct {
	Text      string
	ImageKeys []string
}

// msgParser parses a single Lark message body type into a MsgContent. It returns
// nil for empty or unsupported content of that type.
type msgParser func(rawContent string) *MsgContent

// msgParsers is a registry mapping a Lark message type to its parser. Adding a
// new message type only requires registering a parser here (registry pattern).
var msgParsers = map[string]msgParser{
	"text":        parseTextContent,
	"image":       parseImageContent,
	"post":        parsePostContent,
	"interactive": parseInteractiveContent,
	"system":      parseSystemContent,
}

// parseMsgContent parses a message event body into a MsgContent. Returns nil for
// unsupported or empty messages.
func parseMsgContent(msgType, rawContent string) *MsgContent {
	parser, ok := msgParsers[msgType]
	if ok {
		return parser(rawContent)
	}
	// Types without a dedicated parser (file / audio / media / sticker /
	// share_chat / share_user, and any future type) fall back to the shared Lark
	// text extractor so attachment names and placeholders surface instead of the
	// message being silently dropped. It never leaks raw JSON.
	if text := imlark.ExtractMessageText(msgType, rawContent); text != "" {
		return &MsgContent{Text: text}
	}
	return nil
}

// parseMessageWithMentions parses a message body and substitutes the platform's
// @-mention placeholders with the names they stand for.
//
// It is the parser for any message whose body did not arrive with this process
// — a quoted message, a history entry — because such a body still carries the
// placeholders Lark leaves in the text ("@_user_1") and the names only in the
// mention list beside it. Feeding the placeholders to a model is what this
// exists to prevent.
//
// The placeholder the platform reported is preferred over the index-derived one
// because the mention list is not guaranteed to be in the same order as the
// text; the index is the fallback for a body that carries a mention without its
// key, which is what the platform does for some message types.
func parseMessageWithMentions(msgType, rawContent string, mentions []*larkim.Mention) *MsgContent {
	mc := parseMsgContent(msgType, rawContent)
	if mc == nil || mc.Text == "" {
		return mc
	}
	for i, m := range mentions {
		name := ptr.From(m.Name)
		if name == "" {
			continue
		}
		token := ptr.From(m.Key)
		if token == "" {
			token = fmt.Sprintf("@_user_%d", i+1)
		}
		mc.Text = strings.ReplaceAll(mc.Text, token, "@"+name)
	}
	return mc
}

// parseTextContent parses a plain "text" message body.
func parseTextContent(rawContent string) *MsgContent {
	var m map[string]string
	if err := json.Unmarshal([]byte(rawContent), &m); err != nil {
		return nil
	}
	text := strings.TrimSpace(m["text"])
	if text == "" {
		return nil
	}
	return &MsgContent{Text: text}
}

// parseImageContent parses an "image" message body.
func parseImageContent(rawContent string) *MsgContent {
	var m map[string]string
	if err := json.Unmarshal([]byte(rawContent), &m); err != nil {
		return nil
	}
	key := m["image_key"]
	if key == "" {
		return nil
	}
	return &MsgContent{ImageKeys: []string{key}}
}

// parsePostContent parses a rich-text "post" message body.
//
// # Pictures as well as words
//
// A post carries text and images in one message, and this used to keep only the
// text: a user who wrote a sentence and attached a screenshot had the sentence
// answered and the screenshot silently dropped. Both are returned now, and the
// funnel sends both — see MsgContent's two fields, which exist precisely so a
// message can be both.
func parsePostContent(rawContent string) *MsgContent {
	text := extractPostText(rawContent)
	keys := extractPostImages(rawContent)
	if text == "" && len(keys) == 0 {
		return nil
	}
	return &MsgContent{Text: text, ImageKeys: keys}
}

// extractPostImages collects the image_key of every picture in a rich-text body.
//
// It walks the same two shapes extractPostText does — a bare {title, content}
// document and one wrapped per language — because a client that sends one shape
// for text sends the same shape for pictures.
func extractPostImages(content string) []string {
	if content == "" {
		return nil
	}

	// The bare shape.
	var bare struct {
		Content [][]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &bare); err == nil {
		if keys := postLineImages(bare.Content); len(keys) > 0 {
			return keys
		}
	}

	// The per-language shape. Only the first language block is read, matching
	// extractPostText: the blocks are translations of one message, and reading
	// every one would offer the same picture once per language.
	var outer map[string]struct {
		Content [][]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &outer); err != nil {
		return nil
	}
	for _, lang := range outer {
		return postLineImages(lang.Content)
	}
	return nil
}

// postLineImages pulls the image keys out of a post's lines.
func postLineImages(lines [][]json.RawMessage) []string {
	var keys []string
	for _, line := range lines {
		for _, raw := range line {
			var node struct {
				Tag      string `json:"tag"`
				ImageKey string `json:"image_key"`
			}
			if err := json.Unmarshal(raw, &node); err != nil {
				continue
			}
			// Only tag "img" names a downloadable picture. An "a" node with an
			// href is a link, and a "media" node is an uploaded file — neither
			// has an image_key this path could fetch.
			if node.Tag == "img" && node.ImageKey != "" {
				keys = append(keys, node.ImageKey)
			}
		}
	}
	return keys
}

// parseInteractiveContent parses an "interactive" card message body.
//
// The card is decoded into the larkcard model and rendered by the model's own
// RenderText, which is the same call that puts a card into the TUI: a card's
// words are defined once. Pictures are carried through as well — MsgContent has
// two fields precisely so a message can be words and images at once, and a card
// with a chart on it is exactly that.
//
// A card whose content cannot be read at all — a remote template, a cardkit
// entity, a body this process cannot parse — still yields its placeholder rather
// than nothing, because "there was a card here" is what the reply is about.
func parseInteractiveContent(rawContent string) *MsgContent {
	card, _, err := larkcard.ParseCard(rawContent)
	if err != nil {
		return &MsgContent{Text: larkcard.CardPlaceholder}
	}
	text := strings.TrimSpace(card.RenderText())
	if text == "" {
		text = larkcard.CardPlaceholder
	}
	return &MsgContent{Text: text, ImageKeys: card.ImageKeys()}
}

// parseSystemContent parses a "system" message body.
func parseSystemContent(rawContent string) *MsgContent {
	return &MsgContent{Text: parseSystemMessage(rawContent)}
}

// cleanFeishuAutoLinks strips Feishu auto-generated hyperlink formatting.
func cleanFeishuAutoLinks(text string) string {
	re := regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	return re.ReplaceAllString(text, "$1")
}

// isMentioningHelper reports whether the bot is @-mentioned in the message.
func isMentioningHelper(dr *runtime.DynamicRuntime) bool {
	for _, m := range dr.Mentions {
		if m.IsBot {
			return true
		}
	}
	return false
}

// normalizeQuestion turns a message body into the question a model is asked.
//
// It is the single place that does so, and it takes no notice of where the
// message came from: the same sentence sent in a P2P conversation and sent in a
// group while @-mentioning the bot has to arrive at the model as the same
// string, or the two conversations answer the same words differently. Calling
// it on both paths is what makes that true; a caller-side branch would put the
// difference back.
//
// The bot's own mention is removed rather than kept as a form of address — the
// model is already being addressed, and a leftover "@bot" only invites it to
// greet the bot. Which mention is the bot is taken from IsBot, the platform's
// own answer (imlark.IsBotMention), rather than from comparing a name against
// the configured bot name: a human whose display name happens to equal the
// bot's is not the bot and must survive, and a bot whose Lark display name
// differs from the configured one must still go.
//
// It runs before the command rung of the funnel, not merely before the prompt
// rung. In a group the bot is addressed by @-mentioning it, so the mention is
// still attached to the front of "/pipeline …" when the text arrives; strip it
// too late and the command never looks like a command, and the model — not the
// command — decides whether to run it and with what arguments.
//
// The trim is at the end for the same reason. Trimming first and stripping
// afterwards leaves the space that separated the mention from the sentence, and
// a leading space is enough to hide a command from a prefix test.
func normalizeQuestion(text string, mentions []runtime.MentionedUser) string {
	for i, m := range mentions {
		// The placeholder is preferred over the name: it marks a position in the
		// body, so it cannot be confused with the same word appearing as prose.
		// The index fallback reconstructs it for callers that carry names only.
		token := m.Key
		if token == "" {
			token = fmt.Sprintf("@_user_%d", i+1)
		}
		// Strip the placeholder unconditionally as well as the name: the
		// platform's placeholder survives into the text when the mention has no
		// display name (imlark.ApplyMentions leaves it rather than emit a bare
		// "@"), and by then it is the only spelling present.
		switch {
		case m.IsBot:
			text = cutMention(text, token)
			text = cutMention(text, "@"+m.Name)
		case m.Name == "":
			// A mention nobody can name is dropped rather than rendered as "@",
			// which reads as a sentence the user never wrote.
			text = cutMention(text, token)
		default:
			text = strings.ReplaceAll(text, token, "@"+m.Name)
		}
	}
	return strings.TrimSpace(text)
}

// cutMention removes a mention token together with one adjacent space, so that
// "@bot question", "question @bot" and a lone "@bot" all leave "question". The
// following space is consumed first because that is where the composer puts it
// and where the trailing trim cannot help.
func cutMention(text, token string) string {
	if token == "" {
		return text
	}
	if cut := strings.ReplaceAll(text, token+" ", ""); cut != text {
		return cut
	}
	return strings.ReplaceAll(strings.ReplaceAll(text, " "+token, ""), token, "")
}
