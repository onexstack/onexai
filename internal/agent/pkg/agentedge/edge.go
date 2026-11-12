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

package agentedge

import (
	"context"
	"log/slog"
	"path"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/pkg/netguard"
	"github.com/onexstack/onexai/pkg/media"
)

// inlinerKey is the context key the resolver is published under.
//
// The inliner travels on the context rather than in a struct field because the
// two places that need it are a model wrapper and a middleware, both of which
// are built from the agent set alone and reused across runs — see the note on
// runState in the eino engine. A field would make them a function of the run.
type inlinerKey struct{}

// WithInliner publishes the resolver for the model edge to find.
func WithInliner(ctx context.Context, in *Inliner) context.Context {
	if in == nil || !in.Available() {
		return ctx
	}
	return context.WithValue(ctx, inlinerKey{}, in)
}

// InlinerFrom returns the resolver published on ctx, or nil.
func InlinerFrom(ctx context.Context) *Inliner {
	in, _ := ctx.Value(inlinerKey{}).(*Inliner)
	return in
}

// ResolveParts returns parts with every image reference replaced by what the
// model can read, reporting whether anything changed.
//
// It is the schema-message counterpart of Inline: the same resolution, applied
// where the messages are about to be handed to a model rather than where they
// were loaded. Doing it here — in a wrapper around the model call — is what
// keeps the resolved bytes out of the agent's state, and therefore out of the
// checkpoint a paused run writes.
//
// A part that is already model-ready (a data URL, or an http URL the provider
// fetches itself) is passed through untouched. Resolving one would be wrong as
// well as wasteful: a remote image the model is meant to fetch would become one
// this service fetches, or be dropped when there is no fetcher configured.
//
// A reference that cannot be resolved becomes a text part saying so, in place,
// rather than being dropped silently — the same note Inline appends, for the
// same reason: a model that is told it could not read a picture gives a better
// answer than one that never learns there was one.
func (in *Inliner) ResolveParts(
	ctx context.Context, parts []schema.MessageInputPart,
) ([]schema.MessageInputPart, bool) {
	if in == nil || !in.Available() || len(parts) == 0 {
		return parts, false
	}

	out := make([]schema.MessageInputPart, 0, len(parts))
	changed := false
	for _, part := range parts {
		url, ok := imagePartURL(part)
		if !ok || modelReady(url) {
			out = append(out, part)
			continue
		}

		ref, err := media.Parse(url)
		if err != nil {
			// A shape this service cannot read is not one a provider can either.
			// The note replaces it so the model knows something was there.
			out = append(out, notePart(failureNote))
			changed = true
			continue
		}
		resolved, missing := in.InlineRefs(ctx, []media.Ref{ref})
		if missing > 0 || len(resolved) == 0 {
			out = append(out, notePart(failureNote))
			changed = true
			continue
		}

		// The image struct is copied before its URL is written. The part shares
		// that pointer with the message the caller holds, and writing through it
		// would put the data URL into the very history the checkpoint stores.
		img := ptr.Clone(part.Image)
		img.URL = &resolved[0].URL
		part.Image = img
		out = append(out, part)
		changed = true
	}
	if !changed {
		return parts, false
	}
	return out, true
}

// AppendFailureNote adds the note about unreadable images to a message's text.
//
// It is exported so every surface that reports an unreadable image uses the
// same words, whether it resolves the reference ahead of the request or inside
// the model wrapper.
func AppendFailureNote(content string, missing int) string {
	if missing <= 0 {
		return content
	}
	return appendNote(content, failureNote, missing)
}

// imagePartURL returns the URL of an image part, if it is one.
func imagePartURL(part schema.MessageInputPart) (string, bool) {
	if part.Type != schema.ChatMessagePartTypeImageURL || part.Image == nil || part.Image.URL == nil {
		return "", false
	}
	return *part.Image.URL, true
}

// notePart renders a note about an unreadable image as a text part.
func notePart(note string) schema.MessageInputPart {
	return schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: note}
}

// modelReady reports whether a URL is one a model endpoint can be given.
func modelReady(url string) bool {
	_, ok := media.Ref{URL: url}.ModelReadyURL()
	return ok
}

// maxDocumentBytes bounds what one attached document may add to a prompt.
//
// A document is prompt material, and a prompt that a model truncates anyway is
// better truncated here, where the note says so, than cut mid-sentence by the
// provider.
const maxDocumentBytes = 256 << 10

// ReadDocument resolves a text-like document reference to its text.
//
// It is deliberately separate from InlineRefs: an image becomes a data URL the
// model reads as bytes, while a document becomes text appended to the question.
// The two cannot share a path without one of them being wrong.
func (in *Inliner) ReadDocument(ctx context.Context, ref media.Ref) (string, bool) {
	if in == nil || !in.Available() {
		return "", false
	}
	if strings.HasPrefix(ref.URL, "data:") {
		data, err := media.DecodeDataURL(ref.URL)
		if err != nil {
			return "", false
		}
		return documentText(data, ref), true
	}
	// Resolved without the image path: shrink and the byte cache exist for
	// pictures, and running a document through them would mangle it.
	data, mimeType, err := in.read(ctx, ref)
	if err != nil {
		slog.WarnContext(ctx, "document could not be read for the model",
			"asset_id", ref.AssetID, "object_key", ref.ObjectKey,
			"url", netguard.RedactURL(ref.URL), "error", err)
		return "", false
	}
	if ref.MimeType == "" {
		ref.MimeType = mimeType
	}
	return documentText(data, ref), true
}

// documentText applies the size bound and refuses a binary payload.
func documentText(data []byte, ref media.Ref) string {
	if len(data) > maxDocumentBytes {
		data = data[:maxDocumentBytes]
		return string(data) + "\n[document truncated]"
	}
	return string(data)
}

// IsTextDocument reports whether a media type names a document this service can
// hand to a model as text.
func IsTextDocument(mediaType, name string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "text/plain", "text/markdown", "text/csv", "text/html", "text/xml",
		"application/json", "application/xml", "application/yaml", "application/x-yaml":
		return true
	case "":
		switch strings.ToLower(path.Ext(name)) {
		case ".txt", ".md", ".csv", ".json", ".yaml", ".yml", ".xml", ".log":
			return true
		}
	}
	return false
}
