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

package agent

import (
	"context"
	"log/slog"
	"strings"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

// migrateInlineImages moves base64 images out of a loaded history and into the
// store, returning the rewritten history.
//
// # Why a lazy migration rather than a job
//
// Sessions written before images became references hold data URLs, and those
// sessions are the reason the change was worth making: each one is a base64
// payload the store reads back in full on every turn of that conversation.
// There are only two ways to get rid of them — sweep every session in Redis, or
// convert each one the first time it is read. The second needs no list of
// session keys, no downtime, and no coordination between pods, and it converts
// exactly the sessions that are still in use: an abandoned conversation costs
// nothing to leave alone until its TTL takes it.
//
// # What it does not do
//
// It does not fail the turn. A session that cannot be migrated is answered from
// the data URLs it holds — which is what every turn of that session did before
// this code existed — so a store outage degrades to the old behaviour rather
// than to an error.
func (b *AgentBiz) migrateInlineImages(
	ctx context.Context, req *agentapi.AskRequest, history []agentapi.Message,
) []agentapi.Message {
	if b.deps.Assets == nil || !b.deps.Assets.Configured() || len(history) == 0 {
		return history
	}

	changed := false
	for i := range history {
		if len(history[i].Images) == 0 {
			continue
		}
		refs := make([]media.Ref, 0, len(history[i].Images))
		for _, ref := range history[i].Images {
			if !strings.HasPrefix(ref.URL, "data:") {
				refs = append(refs, ref)
				continue
			}
			stored, err := b.storeInlineImage(ctx, req, ref)
			if err != nil {
				// The original reference is kept, so the turn can still read the
				// image. The alternative — dropping it — would turn a storage
				// problem into a conversation that quietly loses its pictures.
				slog.WarnContext(ctx, "could not migrate an inline image out of the session history",
					"session", req.SessionID, "error", err)
				refs = append(refs, ref)
				continue
			}
			refs = append(refs, stored)
			changed = true
		}
		history[i].Images = refs
	}

	if !changed {
		return history
	}
	if err := b.deps.Sessions.Save(ctx, req.SessionID, history); err != nil {
		// The turn proceeds with the migrated history either way; what is lost
		// is the ability to have written it down, which costs one repeated
		// migration on the next turn and nothing else.
		slog.WarnContext(ctx, "could not persist the migrated session history",
			"session", req.SessionID, "error", err)
	}
	return history
}

// storeInlineImage stores the bytes of one legacy inline reference.
//
// The owner is the asker rather than whoever sent the image originally: the
// row records who may read it back, and the only identity this path knows is
// the one on the request. It is the same identity the session is keyed by, so
// the answer is consistent with who can reach the conversation at all.
func (b *AgentBiz) storeInlineImage(
	ctx context.Context, req *agentapi.AskRequest, ref media.Ref,
) (media.Ref, error) {
	data, err := media.DecodeDataURL(ref.URL)
	if err != nil {
		return media.Ref{}, err
	}
	name := ref.Name
	if name == "" {
		name = "inline" + media.ExtensionFor(ref.MimeType)
	}
	return b.deps.Assets.Store(ctx, media.KindImage, req.Subject.ID, name, media.SourceUpload, data)
}
