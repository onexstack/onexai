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

package agentapi

import (
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// The conversions between media.Ref and the contract's ImageRef live here, as
// one pair, because they are the same nine fields in both directions and every
// caller in this service needs at least one of them. Spelled out per call site
// — which is how they were written first — a field added to a reference is a
// change in five places, and the one that is missed is a field that quietly
// stops travelling.
//
// They sit in this package rather than in media because media must not know the
// contract: a reference is the same reference whichever protocol carries it,
// and a leaf helper beside assetstore would be the alternative if a second
// service ever needed the same mapping.

// RefToProto maps a reference onto the contract message.
func RefToProto(r media.Ref) *v1.ImageRef {
	return &v1.ImageRef{
		AssetId:   r.AssetID,
		ObjectKey: r.ObjectKey,
		Url:       r.URL,
		MimeType:  r.MimeType,
		Size:      r.Size,
		Width:     int32(r.Width),
		Height:    int32(r.Height),
		Name:      r.Name,
		Source:    r.Source,
	}
}

// RefsToProto maps a slice of references, returning nil for an empty slice so
// an absent list is absent on the wire rather than an empty one.
func RefsToProto(refs []media.Ref) []*v1.ImageRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]*v1.ImageRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, RefToProto(r))
	}
	return out
}

// RefFromProto maps one contract message back to a reference, reporting whether
// it named anything.
//
// The bool is not decoration: a structured entry that names nothing is a caller
// bug rather than a value to skip, and every caller has to decide what to do
// about it. Returning it here keeps that decision at the call site, where the
// error can say which entry was at fault.
func RefFromProto(in *v1.ImageRef) (media.Ref, bool) {
	if in == nil {
		return media.Ref{}, false
	}
	r := media.Ref{
		AssetID:   in.GetAssetId(),
		ObjectKey: in.GetObjectKey(),
		URL:       in.GetUrl(),
		MimeType:  in.GetMimeType(),
		Size:      in.GetSize(),
		Width:     int(in.GetWidth()),
		Height:    int(in.GetHeight()),
		Name:      in.GetName(),
		Source:    in.GetSource(),
	}
	return r, !r.IsZero()
}

// RefsFromProto maps a slice of contract messages, skipping entries that name
// nothing.
//
// Skipping is right here, unlike at the request boundary (handler/media.go
// errors on an empty entry): a response is not something the caller can fix by
// resending, and one bad entry must not hide the images beside it.
func RefsFromProto(in []*v1.ImageRef) []media.Ref {
	if len(in) == 0 {
		return nil
	}
	out := make([]media.Ref, 0, len(in))
	for _, p := range in {
		if r, ok := RefFromProto(p); ok {
			out = append(out, r)
		}
	}
	return out
}
