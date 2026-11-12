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

// Package agentedge resolves an image reference into something a model can
// read, at the one place where that is still possible: the last step before the
// engine runs.
//
// # Why the resolution is here and not earlier
//
// A conversation's history is stored as references, because a stored data URL
// is a base64 payload the session store reads back in full on every turn of
// that conversation. Resolving a reference therefore has to happen *after* the
// history is loaded and *before* the model is called — and the result must be a
// copy, because the original is what gets written back.
//
// That constraint is the whole design: Inline returns new messages and never
// touches its input, so the caller can hand one slice to the engine and the
// other to the store without either being able to affect the other.
package agentedge

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/onexstack/onexstack/pkg/store/where"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"

	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/netguard"
	"github.com/onexstack/onexai/pkg/media"
)

// failureNote is appended to a message whose image could not be read.
//
// The model is told rather than left to answer a question about an image it
// never received. Silently dropping it produces an answer that reads as if the
// image had been seen and found unremarkable, which is worse than an answer
// that says it could not be read — and both are worse than the answer the user
// expected, which is why the turn still runs.
const failureNote = "（有一张图片未能读取）"

// cacheBudgetBytes bounds the resolved-image cache.
//
// It is small on purpose. The cache exists to stop the same image being decoded
// and base64-encoded once per turn of a conversation, which is the common case;
// it is not a content store. Sixteen mebibytes holds a handful of screenshots
// and cannot grow with the number of live conversations.
const cacheBudgetBytes = 16 << 20

// Deps are the Inliner's dependencies.
type Deps struct {
	// Metrics records what the model edge cost. Nil is a no-op, which is what a
	// test gets. See the package comment for why the reset is a recording and
	// not just a cache.
	Metrics *metrics.Metrics
	// Bundle reads bytes this service stores. Nil disables every path that
	// resolves to this service's own store.
	Bundle *media.Bundle
	// Rows resolves an asset id to the object key its bytes are at. Nil means
	// a reference carrying only an id cannot be resolved.
	Rows agentstore.AssetStore
	// Fetcher reads an image from a URL. Nil disables the remote-URL path, and
	// a deployment that expects none should leave it nil rather than run a
	// fetcher it never uses.
	Fetcher *netguard.Fetcher
	// MaxPixels, when positive, is the largest image the model is shown: a
	// larger one is shrunk to fit before it is inlined.
	//
	// Zero disables shrinking, and that is the default. It is a lossy step —
	// small text in a screenshot is exactly what a downscale takes away, and a
	// screenshot is what this bot is most often sent — so it is a deployment's
	// decision rather than something this code should do quietly.
	MaxPixels int
}

// Inliner resolves references into data URLs.
type Inliner struct {
	bundle  *media.Bundle
	rows    agentstore.AssetStore
	fetcher *netguard.Fetcher
	// maxPixels is the model-edge ceiling; zero means do not shrink.
	maxPixels int
	metrics   *metrics.Metrics

	cacheMu   sync.Mutex
	cache     map[string][]byte
	cacheMime map[string]string
	cacheSize int64
	// cacheOrder is the insertion order, used to evict the oldest entries.
	// A full LRU would be more accurate and more code; this is FIFO, which for
	// a cache keyed by image is the same thing in the common case, where an
	// entry is read many times and then never again.
	cacheOrder []string
}

// New builds an Inliner.
func New(deps Deps) *Inliner {
	return &Inliner{
		bundle:    deps.Bundle,
		rows:      deps.Rows,
		fetcher:   deps.Fetcher,
		maxPixels: deps.MaxPixels,
		metrics:   deps.Metrics,
		cache:     map[string][]byte{},
		cacheMime: map[string]string{},
	}
}

// Available reports whether anything can be resolved at all. A deployment with
// no store and no fetcher has nothing to resolve, and the caller can skip the
// copy it would otherwise make.
func (in *Inliner) Available() bool {
	return in != nil && (in.bundle.Configured() || in.fetcher != nil)
}

// Inline returns a copy of msgs whose image references are model-ready.
//
// The input is not modified. Every reference in the result has its URL set to
// what the model should be given: a data URL for an image this service holds,
// or the original http URL when there is nothing this service can do with it
// and the model may be able to fetch it itself.
//
// A reference that cannot be resolved is dropped and noted in the message's
// text. Dropping it rather than failing the turn is deliberate: the question
// the user asked is still answerable, and refusing to answer it because one of
// several images was unreadable loses more than it protects.
func (in *Inliner) Inline(ctx context.Context, msgs []agentapi.Message) []agentapi.Message {
	if !in.Available() {
		return msgs
	}

	out := make([]agentapi.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		if len(out[i].Images) == 0 {
			continue
		}
		// The slice is copied before it is written to. The input's own backing
		// array is shared with the caller's history, which is about to be
		// written back to the store.
		refs := make([]media.Ref, len(out[i].Images))
		copy(refs, out[i].Images)

		resolved := make([]media.Ref, 0, len(refs))
		missing := 0
		for _, ref := range refs {
			data, mimeType, err := in.resolve(ctx, ref)
			if err != nil {
				in.metrics.ObserveImage("inline", "failed", 0)
				slog.WarnContext(ctx, "image could not be read for the model",
					"asset_id", ref.AssetID, "object_key", ref.ObjectKey,
					"url", netguard.RedactURL(ref.URL), "error", err)
				missing++
				continue
			}
			ref.URL = media.DataURL(mimeType, data)
			if ref.MimeType == "" {
				ref.MimeType = mimeType
			}
			in.metrics.ObserveImage("inline", "ok", int64(len(data)))
			resolved = append(resolved, ref)
		}

		out[i].Images = resolved
		if missing > 0 {
			out[i].Content = appendNote(out[i].Content, failureNote, missing)
		}
	}
	return out
}

// InlineRefs resolves a bare list of references, for a caller assembling a
// request rather than a history.
func (in *Inliner) InlineRefs(ctx context.Context, refs []media.Ref) ([]media.Ref, int) {
	if len(refs) == 0 || !in.Available() {
		return refs, 0
	}
	out := make([]media.Ref, 0, len(refs))
	missing := 0
	for _, ref := range refs {
		data, mimeType, err := in.resolve(ctx, ref)
		if err != nil {
			in.metrics.ObserveImage("inline", "failed", 0)
			slog.WarnContext(ctx, "image could not be read for the model",
				"asset_id", ref.AssetID, "object_key", ref.ObjectKey,
				"url", netguard.RedactURL(ref.URL), "error", err)
			missing++
			continue
		}
		ref.URL = media.DataURL(mimeType, data)
		if ref.MimeType == "" {
			ref.MimeType = mimeType
		}
		in.metrics.ObserveImage("inline", "ok", int64(len(data)))
		out = append(out, ref)
	}
	return out, missing
}

// resolve returns the bytes of a reference and their media type.
//
// The order is by how much is known about the reference: a key is this
// service's own address and needs no lookup, an id needs the row that holds
// the key, and a URL is whatever the URL says — including one this service
// serves itself, which is recognised by its path and read from disk rather than
// fetched over the network.
func (in *Inliner) resolve(ctx context.Context, ref media.Ref) ([]byte, string, error) {
	// An already-inlined reference is not re-read. A history written by an
	// earlier turn of this same process — or a client that sent a data URL —
	// carries its bytes already, and decoding base64 only to re-encode it is
	// work with no effect.
	if strings.HasPrefix(ref.URL, "data:") {
		data, err := media.DecodeDataURL(ref.URL)
		if err != nil {
			return nil, "", err
		}
		mimeType := ref.MimeType
		if mimeType == "" {
			mimeType = media.SniffImageType(data, "")
		}
		return data, mimeType, nil
	}

	if hit, mimeType, ok := in.cached(ref); ok {
		return hit, mimeType, nil
	}

	data, mimeType, err := in.read(ctx, ref)
	if err != nil {
		return nil, "", err
	}
	// Shrunk before it is cached, so the cache holds what the model is sent
	// rather than the original: a second turn pays neither the read nor the
	// downscale again.
	data, mimeType = in.shrink(ctx, ref, data, mimeType)
	in.remember(ref, data, mimeType)
	return data, mimeType, nil
}

// shrink enforces the model-edge ceiling, and reports the media type of what it
// returns.
//
// It never fails a turn. A picture that cannot be decoded is one that cannot be
// shrunk, and sending the original is a better answer than sending nothing —
// the ceiling exists to bound a cost, not to be a gate.
func (in *Inliner) shrink(ctx context.Context, ref media.Ref, data []byte, mimeType string) ([]byte, string) {
	if in.maxPixels <= 0 {
		return data, mimeType
	}
	out, outType, err := media.Resize(data, in.maxPixels)
	if err != nil {
		slog.WarnContext(ctx, "could not shrink an image for the model",
			"asset_id", ref.AssetID, "error", err)
		return data, mimeType
	}
	if len(out) < len(data) {
		slog.DebugContext(ctx, "shrunk an image for the model",
			"asset_id", ref.AssetID, "from_bytes", len(data), "to_bytes", len(out))
	}
	return out, outType
}

// read obtains the bytes from whichever source the reference names.
//
// # The order is cheapest-first, and that is a decision rather than an accident
//
// A key needs no lookup. A URL on this service's own read path also needs none:
// the key is in the path, so the object can be opened directly. An id is the
// one shape that requires a database read, and it is what every reference from
// the chat service arrives as — those carry no key, because the upload response
// describes an asset rather than addressing an object.
//
// So the self-served URL is tried before the id. On the common path that turns
// one row read per image per turn into none, and the bytes are the same either
// way: the store is this process's own, and both spellings name the same
// object.
func (in *Inliner) read(ctx context.Context, ref media.Ref) ([]byte, string, error) {
	if ref.ObjectKey != "" {
		return in.fromStore(ctx, ref.ObjectKey, ref)
	}
	if key, ok := objectKeyFromURL(ref.URL); ok {
		return in.fromStore(ctx, key, ref)
	}
	if ref.AssetID != "" {
		if in.rows == nil {
			return nil, "", media.ErrNotConfigured
		}
		row, err := in.rows.Get(ctx, where.F("id", ref.AssetID))
		if err != nil {
			return nil, "", err
		}
		return in.fromStore(ctx, row.ObjectKey, ref)
	}

	if in.fetcher == nil {
		return nil, "", media.ErrNotConfigured
	}
	data, contentType, err := in.fetcher.Get(ctx, ref.URL)
	if err != nil {
		return nil, "", err
	}
	return data, pickMime(ref.MimeType, contentType, data), nil
}

// fromStore reads an object this service stores.
func (in *Inliner) fromStore(ctx context.Context, objectKey string, ref media.Ref) ([]byte, string, error) {
	if !in.bundle.Configured() {
		return nil, "", media.ErrNotConfigured
	}
	data, mimeType, err := in.bundle.OpenBytes(ctx, media.Ref{
		ObjectKey: objectKey,
		MimeType:  ref.MimeType,
	})
	if err != nil {
		return nil, "", err
	}
	return data, pickMime(mimeType, "", data), nil
}

// objectKeyFromURL recognises this service's own public read path.
//
// It exists so that a reference stored as a URL and read back on the same host
// is served from the store rather than fetched over the network: the fetch
// would work, but it would spend a socket, a DNS lookup and a request's worth
// of server resources to read a file this process can open directly.
func objectKeyFromURL(raw string) (string, bool) {
	rest, ok := strings.CutPrefix(raw, media.PublicPath)
	if !ok || rest == "" {
		return "", false
	}
	// The signature, when there is one, is about the path and is checked by the
	// route that serves it — not by this read.
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest = rest[:i]
	}
	if strings.Contains(rest, "..") {
		return "", false
	}
	return rest, true
}

// pickMime chooses a media type from the candidates, sniffing as a last resort.
//
// The order is the same principle as SniffImageType: a declared or
// previously-recorded type is used only when there is nothing better, and the
// bytes are consulted only when no candidate is usable at all. A type that is
// empty here would produce an octet-stream data URL, which some model
// endpoints reject — so an answer is always produced.
func pickMime(candidates ...any) string {
	var data []byte
	for _, c := range candidates {
		switch v := c.(type) {
		case string:
			if ct := media.NormalizeContentType(v); strings.HasPrefix(ct, "image/") {
				return ct
			}
		case []byte:
			data = v
		}
	}
	if data != nil {
		if ct := media.SniffImageType(data, ""); strings.HasPrefix(ct, "image/") {
			return ct
		}
	}
	return "image/png"
}

// appendNote adds a note about unreadable images to a message's text.
//
// The count is included because "one of the three images failed" and "all three
// failed" call for different confidence in the answer, and the model has no
// other way to tell them apart.
func appendNote(content, note string, count int) string {
	if count > 1 {
		note = strings.Replace(note, "一张图片", strconv.Itoa(count)+"张图片", 1)
	}
	if content == "" {
		return note
	}
	return content + " " + note
}

// cached returns previously resolved bytes, or false.
func (in *Inliner) cached(ref media.Ref) ([]byte, string, bool) {
	key := ref.CacheKey()
	if key == "" {
		return nil, "", false
	}
	in.cacheMu.Lock()
	defer in.cacheMu.Unlock()
	data, ok := in.cache[key]
	if !ok {
		return nil, "", false
	}
	return data, in.cacheMime[key], true
}

// remember stores resolved bytes under a byte budget.
//
// The bytes are shared with the caller rather than copied. That is safe because
// nothing mutates them: the only thing done with them is base64-encoding into a
// new string, and a caller that changed them would be changing a slice it has
// no reason to hold on to.
func (in *Inliner) remember(ref media.Ref, data []byte, mimeType string) {
	key := ref.CacheKey()
	if key == "" || int64(len(data)) > cacheBudgetBytes {
		return
	}

	in.cacheMu.Lock()
	defer in.cacheMu.Unlock()
	if _, exists := in.cache[key]; exists {
		return
	}
	for in.cacheSize+int64(len(data)) > cacheBudgetBytes && len(in.cacheOrder) > 0 {
		oldest := in.cacheOrder[0]
		in.cacheOrder = in.cacheOrder[1:]
		if prev, ok := in.cache[oldest]; ok {
			in.cacheSize -= int64(len(prev))
			delete(in.cache, oldest)
			delete(in.cacheMime, oldest)
		}
	}
	in.cache[key] = data
	in.cacheMime[key] = mimeType
	in.cacheOrder = append(in.cacheOrder, key)
	in.cacheSize += int64(len(data))
}
