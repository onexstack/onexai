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

// Package media gives an image an identity inside onexai.
//
// # One form for an image, and it is not the bytes
//
// Every surface that can receive an image — the Lark webhook, the WS client,
// the OpenAI-compatible endpoint, a tool that generates one — produces a Ref.
// A Ref is an identity plus the metadata its producer already knew. It is
// deliberately never the bytes.
//
// The reason is where the bytes would otherwise go. Before this package, an
// image travelled as a base64 data URL: through the ask request, into the
// session history, and out to the model on every following turn. A 3 MiB
// screenshot became a 4 MiB string that Redis loaded on every turn of that
// conversation, and nothing could be deduplicated, authorised, or expired,
// because a data URL has no identity to hang any of those on.
//
// A Ref has one. The bytes live in a Store, are read once per turn at the one
// place that needs them (the model edge), and are never written to a session.
//
// # What a Ref is not
//
// It is not a promise that the bytes are reachable. It is a name, and
// resolving it is a separate step with its own failure — see Bundle.Open and
// the agent edge's inliner.
package media

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Scheme prefixes for the two compact reference forms.
//
// They exist so a reference can ride the wire in a plain string: the fields
// that carry images today are `repeated string` (AgentAskRequest.images,
// ChatCompletionMessage.images), and "asset:img_abc" costs those callers
// nothing to adopt, where a restructured message would cost every one of them
// a release. See Parse.
const (
	// AssetScheme prefixes an asset id issued by this service.
	AssetScheme = "asset:"
	// ObjectScheme prefixes a raw object key inside the configured store.
	ObjectScheme = "media:"
)

// Source values record where a Ref came from. They are diagnostic: a log line
// or an asset row that says "lark" answers "why is this here" without reading
// the call site.
const (
	SourceUpload    = "upload"
	SourceLark      = "lark"
	SourceURL       = "url"
	SourceGenerated = "generated"
)

// Ref is the one form an image takes once it enters onexai.
//
// The fields are a superset of what any single producer knows: a Lark image
// arrives as bytes and fills Size and MimeType, a generated image fills Width
// and Height, a forwarded URL fills only URL. Empty means "not known", not
// "not applicable", which is why every field is omitempty on the wire.
type Ref struct {
	// AssetID is this service's identity for the object ("img_…"). It is what
	// authorisation and expiry are keyed by, so a client that forwards one it
	// did not upload can be refused.
	AssetID string `json:"asset_id,omitempty"`
	// ObjectKey is the store-relative key. It is carried so a service that can
	// reach the store directly need not round-trip through the asset table.
	ObjectKey string `json:"object_key,omitempty"`
	// URL is the object's address on whichever store holds it. It is absolute for a remote object,
	// and a relative "/media/…" path when this service serves it itself.
	URL string `json:"url,omitempty"`
	// MimeType is the media type, e.g. image/png. It is needed at the model
	// edge, where the bytes become a data URL.
	MimeType string `json:"mime_type,omitempty"`
	// Size is the object size in bytes. Zero means unknown.
	Size int64 `json:"size,omitempty"`
	// Width and Height are pixels. Zero means unknown or not an image.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// Name is the original filename, for display and for the store's key.
	Name string `json:"name,omitempty"`
	// Source records the producing surface. See the Source* constants.
	Source string `json:"source,omitempty"`
}

// IsZero reports whether the Ref names nothing. A zero Ref is what a caller
// gets from a field that was not set, and treating it as a reference would
// produce a lookup for the empty key.
func (r Ref) IsZero() bool {
	return r.AssetID == "" && r.ObjectKey == "" && r.URL == ""
}

// String renders the compact form a plain-string field can carry.
//
// The order matters: an asset id is the strongest identity — it is what the
// asset table and the authorisation check are keyed by — so it wins over the
// object key, which in turn wins over a raw URL. The result is lossy for a
// fully-populated Ref (the URL and dimensions are dropped), and that is
// intended: the compact form is a *reference*, and everything dropped is
// recoverable from the asset row.
func (r Ref) String() string {
	switch {
	case r.AssetID != "":
		return AssetScheme + r.AssetID
	case r.ObjectKey != "":
		return ObjectScheme + r.ObjectKey
	default:
		return r.URL
	}
}

// Parse reads any of the four wire forms into a Ref.
//
//	asset:<id>            an asset this service issued — the main path
//	media:<key>           a store-relative object key
//	data:image/png;base64,…  a legacy inline image
//	https://…  /media/…   a remote or self-served URL
//
// # Why the legacy forms are still accepted
//
// Two populations still produce them and neither can be migrated in one
// release. Old clients send data URLs because that is all the field ever
// documented; sessions already in Redis hold data URLs because that is what
// the previous code wrote. Refusing either would turn an upgrade into an
// outage, so Parse normalises them into a Ref whose URL is the original string
// — which every consumer already knows how to use.
func Parse(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, fmt.Errorf("media: empty image reference")
	}

	switch {
	case strings.HasPrefix(s, AssetScheme):
		id := strings.TrimPrefix(s, AssetScheme)
		if id == "" {
			return Ref{}, fmt.Errorf("media: %q names no asset", s)
		}
		return Ref{AssetID: id}, nil

	case strings.HasPrefix(s, ObjectScheme):
		key := strings.TrimPrefix(s, ObjectScheme)
		if key == "" {
			return Ref{}, fmt.Errorf("media: %q names no object", s)
		}
		return Ref{ObjectKey: key}, nil

	case strings.HasPrefix(s, "data:"):
		return Ref{URL: s, MimeType: dataURLMime(s), Source: SourceUpload}, nil

	case strings.HasPrefix(s, "/"):
		// A relative path is this service's own static route. It is not
		// validated further here: whether it resolves is the store's answer.
		return Ref{URL: s}, nil
	}

	u, err := url.Parse(s)
	if err != nil {
		return Ref{}, fmt.Errorf("media: %q is not an image reference: %w", s, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Ref{}, fmt.Errorf("media: %q is not an image reference (want data:, http(s):, %s or %s)",
			s, AssetScheme, ObjectScheme)
	}
	return Ref{URL: s, Source: SourceURL}, nil
}

// RefStrings renders references in the compact string form a plain-string field
// carries.
//
// A reference that names nothing renders as the empty string and is dropped
// rather than sent: an empty entry reaches the other side as an image naming no
// object, which it would either refuse — failing a turn over a value the caller
// did not mean to send — or look up, asking the store for the empty key.
// Neither is what a caller meant by leaving a field unset.
func RefStrings(refs []Ref) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if s := r.String(); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ParseMany parses a list, returning the first error.
//
// A malformed entry fails the whole list rather than being skipped. Dropping it
// would answer a question about three images while silently having seen two,
// and the caller has no way to tell that from a request that only mentioned
// two — which is the shape of bug that is found months later.
func ParseMany(ss []string) ([]Ref, error) {
	if len(ss) == 0 {
		return nil, nil
	}
	out := make([]Ref, 0, len(ss))
	for _, s := range ss {
		r, err := Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// ModelReadyURL returns the URL a model endpoint can be given, and whether
// there is one.
//
// # The closed set is the point
//
// A model endpoint fetches the URL itself or decodes it inline, so only two
// shapes work: an inline data URL, and an absolute http(s) URL the provider's
// servers can reach. Everything else this type can hold — a bare asset id, a
// store-relative "/media/…" path, an empty string — is an address only this
// service can turn into bytes, and handing one to a provider produces a request
// that fails at the far end with a message about an invalid URL.
//
// It is a method rather than a check at each engine because both engines need
// the same answer, and a rule restated is a rule that can be restated
// differently.
func (r Ref) ModelReadyURL() (string, bool) {
	switch {
	case strings.HasPrefix(r.URL, "data:"):
		return r.URL, true
	case strings.HasPrefix(r.URL, "http://"), strings.HasPrefix(r.URL, "https://"):
		return r.URL, true
	default:
		return "", false
	}
}

// CacheKey is a stable identity for an in-process cache of resolved bytes.
//
// It prefers the id over the key over the URL for the same reason String does,
// and it falls back to a hash of the URL rather than the URL itself so a data
// URL — megabytes long — does not become a map key that is compared, and
// retained, byte for byte.
//
// Two references to the same object share a key, which is the point: the same
// image sent twice, or carried in a conversation's history across turns,
// resolves once.
func (r Ref) CacheKey() string {
	switch {
	case r.AssetID != "":
		return AssetScheme + r.AssetID
	case r.ObjectKey != "":
		return ObjectScheme + r.ObjectKey
	case r.URL == "":
		return ""
	default:
		sum := sha256.Sum256([]byte(r.URL))
		return "url:" + hex.EncodeToString(sum[:16])
	}
}

// dataURLMime extracts the media type from a data URL, or "".
//
// "data:image/png;base64,AAAA" yields "image/png". The comma is optional in a
// data URL whose payload is empty, so its absence is not treated as malformed:
// the caller gets "" and the model edge falls back to sniffing the bytes.
func dataURLMime(s string) string {
	rest, ok := strings.CutPrefix(s, "data:")
	if !ok {
		return ""
	}
	meta, _, _ := strings.Cut(rest, ",")
	if semi := strings.IndexByte(meta, ';'); semi >= 0 {
		meta = meta[:semi]
	}
	return strings.ToLower(strings.TrimSpace(meta))
}

// DecodeDataURL returns the bytes a data URL carries.
//
// It is here rather than at the model edge because the format — and its two
// encodings — is a property of the reference, not of any one consumer. Base64
// is decoded in both its standard and URL alphabets: the second appears in data
// URLs produced by some browsers, and refusing it would reject an image that a
// browser legitimately sent.
func DecodeDataURL(s string) ([]byte, error) {
	_, payload, ok := strings.Cut(s, ",")
	if !ok {
		return nil, fmt.Errorf("media: data URL has no payload")
	}
	if !strings.Contains(s, ";base64") {
		// The non-base64 form is percent-encoded text, which is not an image.
		// Saying so is more useful than returning the bytes of the literal.
		return nil, fmt.Errorf("media: data URL is not base64-encoded")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil {
		return nil, fmt.Errorf("media: decode data URL: %w", err)
	}
	return data, nil
}

// UnmarshalJSON accepts either form a stored Ref can take.
//
// # Why both
//
// A Ref is written to Redis (session history) and to the chat tables, and both
// held plain strings before this type existed. A bare string is therefore the
// shape of every value already on disk, and it has to keep loading. The object
// form is what this code writes from now on.
func (r *Ref) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "null" {
		*r = Ref{}
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if strings.TrimSpace(s) == "" {
			*r = Ref{}
			return nil
		}
		parsed, err := Parse(s)
		if err != nil {
			return err
		}
		*r = parsed
		return nil
	}

	// The alias drops the methods, so this does not recurse.
	type alias Ref
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*r = Ref(a)
	return nil
}

// MarshalJSON always writes the object form.
//
// It is not symmetric with UnmarshalJSON, and that asymmetry is the migration:
// reading tolerates both shapes so old data keeps working, writing produces
// only the new one so the old shape drains away as sessions expire.
func (r Ref) MarshalJSON() ([]byte, error) {
	type alias Ref
	return json.Marshal(alias(r))
}
