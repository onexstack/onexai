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

package v1

import (
	"encoding/json"
	"fmt"
	"strings"
)

// UnmarshalJSON accepts `content` in either of the two shapes a client may
// send.
//
// # Why this is hand-written
//
// The contract declares `content` as a string and puts images in a field of
// their own, which is what this service has always accepted and what an
// existing caller sends. The OpenAI API declares the same field as *either* a
// string or an array of typed parts, and a stock SDK sends the array as soon as
// an image is attached. Against a plain string field that array fails the bind
// and the whole request is refused — for the one reason the caller cannot work
// around, since their SDK chooses the shape.
//
// So the field is relaxed on the way in. The proto is untouched: `content`
// stays a string on the wire this service writes, and the array form is
// flattened into it plus the images field, which is the shape everything
// downstream already reads.
//
// # Why not a mirror struct
//
// The alternative is a parallel copy of ChatCompletionRequest used only for
// binding, and it would have to be kept in step with the contract by hand — the
// exact drift the generated type exists to prevent. A method on the message
// relaxes one field and leaves every other one generated.
func (x *ChatCompletionMessage) UnmarshalJSON(data []byte) error {
	// The alias drops this method, so the ordinary fields decode through the
	// generated tags and this cannot recurse.
	type alias ChatCompletionMessage

	var wire struct {
		*alias

		Content json.RawMessage `json:"content"`
	}

	wire.alias = (*alias)(x)
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	if len(wire.Content) == 0 {
		return nil
	}

	// A string is the contract's own shape and is taken as-is.
	var plain string

	if err := json.Unmarshal(wire.Content, &plain); err == nil {
		x.Content = plain
		return nil
	}

	// Anything else must be the array of parts. A shape that is neither is
	// refused with what was expected, rather than silently becoming an empty
	// message the model would answer as if nothing had been sent.
	parts, err := decodeContentParts(wire.Content)
	if err != nil {
		return err
	}

	x.Content = parts.text
	// The images found in the array go after whatever the message declared in
	// its own `images` field. Both spellings are read, so a client that sends
	// one of each has both delivered; the order is the declared list first
	// because that is the one the caller wrote deliberately, while the array is
	// what their SDK produced from it.
	x.Images = append(x.GetImages(), parts.images...)
	return nil
}

// contentParts is what an OpenAI content-part array flattens to.
type contentParts struct {
	text   string
	images []string
}

// decodeContentParts reads the array form.
//
// Only two part types are understood, and an unknown one is skipped rather than
// refused: the OpenAI schema has grown fields over time (a file part, an audio
// part), and a request that carries one this service cannot use should still
// deliver the text and pictures it can. Refusing it would turn a caller's
// slightly-newer SDK into an outage.
func decodeContentParts(raw json.RawMessage) (contentParts, error) {
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}

	if err := json.Unmarshal(raw, &parts); err != nil {
		return contentParts{}, fmt.Errorf("content must be a string or an array of parts: %w", err)
	}

	var out contentParts
	var texts []string

	for _, p := range parts {
		switch p.Type {
		case "text", "input_text":
			if p.Text != "" {
				texts = append(texts, p.Text)
			}

		case "image_url", "input_image":
			if url := strings.TrimSpace(p.ImageURL.URL); url != "" {
				out.images = append(out.images, url)
			}
		}
	}

	out.text = strings.Join(texts, "\n")
	return out, nil
}
