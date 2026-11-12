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
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"
)

// Mention is a minimal representation of a Lark @-mention: the placeholder key
// as it appears in the message text (e.g. "@_user_1") and the mentioned user's
// display name.
type Mention struct {
	Key  string
	Name string
}

// ApplyMentions replaces Lark @-mention placeholders in text with the mentioned
// users' real names. Lark puts placeholders like "@_user_1" in the message text
// and the corresponding real names in a separate mentions array (key == the
// placeholder, including the leading "@"). We substitute each key with "@"+name
// so the result still reads as a mention (and stays highlightable).
//
// Longer keys are replaced first so "@_user_10" is not partially matched by
// "@_user_1".
func ApplyMentions(text string, mentions []Mention) string {
	if text == "" || len(mentions) == 0 {
		return text
	}
	// Replace longer keys first to avoid prefix collisions.
	for i := 0; i < len(mentions); i++ {
		for j := i + 1; j < len(mentions); j++ {
			if len(mentions[j].Key) > len(mentions[i].Key) {
				mentions[i], mentions[j] = mentions[j], mentions[i]
			}
		}
	}
	for _, m := range mentions {
		if m.Key == "" {
			continue
		}
		name := m.Name
		if name == "" {
			continue // no real name — leave the placeholder as-is
		}
		text = strings.ReplaceAll(text, m.Key, "@"+name)
	}
	return text
}

// IsBotMention reports whether a Lark mention targets a bot rather than a user.
// Lark's authoritative signal is MentionedType == "bot"; when that field is
// absent (older event shapes) it falls back to the heuristic that a bot carries
// no tenant user_id.
func IsBotMention(m *larkim.MentionEvent) bool {
	if m == nil {
		return false
	}
	if t := ptr.FromOr(m.MentionedType, ""); t != "" {
		return t == "bot"
	}
	return m.Id == nil || ptr.FromOr(m.Id.UserId, "") == ""
}
