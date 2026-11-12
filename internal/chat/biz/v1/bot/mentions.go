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
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"
	"golang.org/x/sync/singleflight"
)

// member is a minimal representation of a conversation member for @-mention
// resolution. It is keyed by open_id on purpose: Lark's outbound mention tag
// (<at user_id="ou_…">) wants the platform handle, not the identity — this is
// one of the few places an open_id is the right id.
type member struct {
	OpenID string
	Name   string
}

// cachedMembers caches a conversation's member list for @-mention resolution.
type cachedMembers struct {
	byName  map[string]string // display name → open_id (the <at> tag's handle)
	names   []string          // names sorted longest-first (avoid prefix collisions)
	fetched time.Time
}

const memberCacheTTL = 5 * time.Minute

// outboundMemberCache is a process-wide cache of conversation members, keyed by
// chat ID, so "@name" outbound mentions don't hit the Lark API on every send.
var outboundMemberCache = struct {
	mu     sync.Mutex
	byConv map[string]cachedMembers
}{byConv: map[string]cachedMembers{}}

// outboundMemberSF coalesces concurrent cache-miss fetches for the same chat so
// a burst of mentions doesn't stampede the Lark API (singleflight).
var outboundMemberSF singleflight.Group

// applyOutboundMentions rewrites "@display name" tokens in an outgoing message
// into Lark's mention syntax <at user_id="ou_xxx"></at> so they render as real
// @mentions in Feishu (instead of literal text). Unknown "@foo" tokens are left
// untouched.
func (b *botBiz) applyOutboundMentions(ctx context.Context, convID, text string) string {
	if convID == "" || !hasMentionCandidate(text) {
		return text
	}
	cm, ok := b.chatMembersForMention(ctx, convID)
	if !ok {
		return text
	}
	for _, name := range cm.names {
		openID := cm.byName[name]
		if name == "" || openID == "" {
			continue
		}
		text = strings.ReplaceAll(text, "@"+name, fmt.Sprintf(`<at user_id="%s"></at>`, openID))
	}
	return text
}

// hasMentionCandidate reports whether the text contains an "@" that could begin
// a display name.
//
// A literal "@" is not an invitation to fetch the conversation's members.
// Command help text writes "@" as prose — /assign's summary is "将当前工单指派给
// @ 的人员处理" (assign the ticket to the @-mentioned people) — and a bare marker used to drag a members API call into the
// reply path of every /help. Feishu's mention syntax never separates the marker
// from the name, so a space (or the end of the text) is what tells prose apart
// from a mention, and it is a cheaper test than the member cache is a hit.
func hasMentionCandidate(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		rest := text[i+1:]
		if rest == "" {
			continue
		}
		if r, _ := utf8.DecodeRuneInString(rest); !unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// chatMembersForMention returns the conversation's members as a name→open_id map
// (cached per conversation for memberCacheTTL to avoid an API call on every send).
func (b *botBiz) chatMembersForMention(ctx context.Context, convID string) (cachedMembers, bool) {
	if cm, ok := b.cachedMembers(convID); ok {
		return cm, true
	}

	// Coalesce concurrent cache misses for the same conversation into a single
	// Lark API call instead of letting every goroutine fetch independently.
	v, err, _ := outboundMemberSF.Do(convID, func() (any, error) {
		members, err := b.getChatMembers(ctx, convID)
		if err != nil {
			return nil, err
		}
		cm := buildCachedMembers(members)

		outboundMemberCache.mu.Lock()
		outboundMemberCache.byConv[convID] = cm
		outboundMemberCache.mu.Unlock()
		return cm, nil
	})
	if err != nil {
		return cachedMembers{}, false
	}
	return v.(cachedMembers), true
}

// cachedMembers returns the cached members for convID if still within TTL.
func (b *botBiz) cachedMembers(convID string) (cachedMembers, bool) {
	outboundMemberCache.mu.Lock()
	defer outboundMemberCache.mu.Unlock()
	cm, ok := outboundMemberCache.byConv[convID]
	if !ok || time.Since(cm.fetched) >= memberCacheTTL {
		return cachedMembers{}, false
	}
	return cm, true
}

// buildCachedMembers builds a cachedMembers value from the raw member list.
func buildCachedMembers(members []member) cachedMembers {
	cm := cachedMembers{byName: make(map[string]string, len(members)), fetched: time.Now()}
	for _, mm := range members {
		if mm.Name == "" || mm.OpenID == "" {
			continue
		}
		cm.byName[mm.Name] = mm.OpenID
		cm.names = append(cm.names, mm.Name)
	}
	sort.Slice(cm.names, func(i, j int) bool { return len(cm.names[i]) > len(cm.names[j]) })
	return cm
}

// getChatMembers returns the members of a Lark group chat for @-mention
// resolution. On error it returns the error and callers leave @name tokens
// untouched.
func (b *botBiz) getChatMembers(ctx context.Context, convID string) ([]member, error) {
	req := larkim.NewGetChatMembersReqBuilder().
		ChatId(convID).
		MemberIdType("open_id").
		PageSize(100).
		Build()

	resp, err := b.lark().Im.ChatMembers.Get(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lark: get chat members: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("lark: get chat members failed: code=%d msg=%s", resp.Code, resp.Msg)
	}

	members := make([]member, 0)
	if resp.Data != nil {
		for _, it := range resp.Data.Items {
			members = append(members, member{OpenID: ptr.FromOr(it.MemberId, ""), Name: ptr.FromOr(it.Name, "")})
		}
	}
	return members, nil
}
