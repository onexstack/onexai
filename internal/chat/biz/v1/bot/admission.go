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

import "time"

// triggerClaimTTL is how long a message is remembered as having produced a turn.
//
// It is long on purpose. A duplicate arrives because the platform decided the
// first delivery went unanswered — seconds to minutes later, by which time the
// turn may well have finished and its card closed. A claim that expired when the
// turn ended would let exactly that duplicate through, and it is the one that
// does the damage: it re-runs the request the user made once.
const triggerClaimTTL = time.Hour

// triggerClaimSweepInterval bounds how often the table is walked to drop expired
// entries, so the sweep costs one pass a minute rather than one pass a message.
const triggerClaimSweepInterval = time.Minute

// claimTrigger records that messageID has produced a turn for sessionKey, and
// reports whether this call was the one that claimed it.
//
// It is the only authority on whether an inbound message is a new request or the
// platform delivering one that has already been answered, and it is deliberately
// the last place to ask. A turn — and with it a card — is created only inside
// runAITurn, whose sole caller is runAI, so a message that does not get past this
// claim can produce neither. Everywhere else a duplicate could be caught is a
// filter on the way in, and a filter has more ways around it than the effect has
// ways of happening: the platform may re-deliver at any time, a restart empties
// every in-memory filter, and a second delivery channel — the webhook route
// enabled later, another process holding the same app — never passes through the
// first filter at all.
//
// The identity claimed is the user's own unit of work — this message in this
// conversation — rather than the delivery's event ID. An event ID identifies a
// delivery, and a re-delivery carries whatever ID the platform chooses for it.
// "The same message answered twice" is what must not happen, so the message is
// what is remembered.
//
// Both arguments must be present for a claim to mean anything. An empty one gives
// the caller the benefit of the doubt and lets the turn through: a message this
// process cannot identify is not thereby a duplicate, and refusing it would drop
// real work to protect against a repeat that cannot be recognised anyway.
//
// The table is per-process, which is the shape of everything else that serialises
// a conversation (see pkg/session — its pending permission and current run are
// in memory too): one replica per Lark app is the deployment this holds under.
// Entries are not evicted before their TTL, so a busy group cannot push another
// conversation's message out of the table — the failure mode that made a
// bounded, shared, lossy cache the wrong home for this.
func (b *botBiz) claimTrigger(sessionKey, messageID string) (first bool) {
	if sessionKey == "" || messageID == "" {
		return true
	}
	key := sessionKey + "\x00" + messageID
	now := time.Now()

	b.claimsMu.Lock()
	defer b.claimsMu.Unlock()
	// Built on demand rather than only in New, like the other caches here: a bot
	// assembled without that constructor would otherwise panic on the first
	// message instead of answering it.
	if b.claims == nil {
		b.claims = make(map[string]time.Time)
	}
	if b.claimsSweptAt.IsZero() || now.Sub(b.claimsSweptAt) >= triggerClaimSweepInterval {
		for k, claimedAt := range b.claims {
			if now.Sub(claimedAt) > triggerClaimTTL {
				delete(b.claims, k)
			}
		}
		b.claimsSweptAt = now
	}
	// Checked and written under one lock, so two deliveries of the same message
	// racing each other cannot both be told they were first.
	if claimedAt, ok := b.claims[key]; ok && now.Sub(claimedAt) <= triggerClaimTTL {
		return false
	}
	b.claims[key] = now
	return true
}
