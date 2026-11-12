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

// Package mediaparse turns the image fields of a request into media
// references.
//
// It lives outside the handler and the validator because both need the same
// parse: the validator decides whether a request is well formed, and the
// handler needs the references themselves. A second parser would let the two
// disagree about which requests are valid.
package mediaparse

import (
	"errors"
	"fmt"

	agent "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// ErrInvalidImageRef reports a structured image reference that names nothing.
var ErrInvalidImageRef = errors.New("media: an image reference names no asset, key or URL")

// FromRequest merges a request's two image fields into references.
//
// # Why there are two fields
//
// `images` is a repeated string and it is what every existing caller sends and
// what a client can send without a schema change — a reference rides it as
// "asset:<id>". `imageRefs` carries the same thing in the structured form, which
// adds the media type and the dimensions. Both are read, and a caller that sends
// both has both considered, in that order.
//
// A malformed entry anywhere fails the whole request. Skipping it would answer a
// question about three images while having seen two, and nothing downstream
// could tell that apart from a request that only mentioned two — which is the
// shape of bug that surfaces months later as "the model ignored my screenshot".
func FromRequest(legacy []string, structured []*v1.ImageRef) ([]media.Ref, error) {
	if len(legacy) == 0 && len(structured) == 0 {
		return nil, nil
	}

	out := make([]media.Ref, 0, len(legacy)+len(structured))
	refs, err := media.ParseMany(legacy)
	if err != nil {
		return nil, err
	}

	out = append(out, refs...)

	for i, in := range structured {
		ref, ok := agent.RefFromProto(in)
		if !ok {
			// A structured entry that names nothing is a caller bug rather than
			// a value to skip: it was sent deliberately, and dropping it would
			// leave the caller believing an image traveled.
			return nil, fmt.Errorf("%w: imageRefs[%d]", ErrInvalidImageRef, i)
		}

		out = append(out, ref)
	}

	return out, nil
}
