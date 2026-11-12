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

// Package tokenizer counts tokens for the agent's context-budget accounting.
//
// Counts come from tiktoken's cl100k_base BPE, read from tables embedded in the
// binary, so the process never reaches the network to tokenize.
//
// cl100k is not the tokenizer of every model this gateway serves, so counts are
// scaled by safetyFactor. The budget guards must over-count rather than
// under-count: over-counting costs a little context, while under-counting lets
// a request through that the provider then rejects for exceeding its window.
package tokenizer

import (
	"encoding/json"
	"sync"

	"github.com/cloudwego/eino/schema"
	tiktoken "github.com/pkoukk/tiktoken-go"
	tiktokenloader "github.com/pkoukk/tiktoken-go-loader"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

const (
	// encodingName is tiktoken's general-purpose BPE. It is selected by name
	// rather than through EncodingForModel because the served model name is
	// operator-configurable and generally not an OpenAI one, so
	// EncodingForModel would fail to resolve it.
	encodingName = "cl100k_base"

	// safetyFactor over-counts every result, because the served model's real
	// tokenizer differs from cl100k (CJK in particular).
	safetyFactor = 1.15

	// tokensPerImage is the flat allowance charged for one image input. Image
	// payloads arrive as data URLs whose base64 text is orders of magnitude
	// larger than the tokens the provider actually bills, so counting the URL
	// as text would trigger compaction on every image-carrying turn. Providers
	// bill images by resolution, not by encoded length; 1024 is a deliberately
	// generous stand-in that stays conservative without being absurd.
	tokensPerImage = 1024
)

var (
	once    sync.Once
	enc     *tiktoken.Tiktoken
	initErr error
)

// encoding returns the process-wide encoder, installing the offline BPE loader
// on first use.
//
// The loader is a package-level global in tiktoken-go that must be replaced
// before the first encode. Left alone, the library downloads its tables from
// openaipublic.blob.core.windows.net, which is not reachable from the
// deployment this gateway runs in. Holding the ordering inside sync.Once means
// no caller can get it wrong.
func encoding() (*tiktoken.Tiktoken, error) {
	once.Do(func() {
		tiktoken.SetBpeLoader(tiktokenloader.NewOfflineLoader())
		enc, initErr = tiktoken.GetEncoding(encodingName)
	})
	return enc, initErr
}

// scale rounds a raw count up by safetyFactor.
func scale(n int) int {
	return int(float64(n)*safetyFactor) + 1
}

// Count returns the scaled token count of text.
func Count(text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	e, err := encoding()
	if err != nil {
		return 0, err
	}
	return scale(len(e.EncodeOrdinary(text))), nil
}

// CountAll returns the scaled token count of the concatenated texts.
func CountAll(texts ...string) (int, error) {
	e, err := encoding()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, t := range texts {
		if t == "" {
			continue
		}
		total += len(e.EncodeOrdinary(t))
	}
	if total == 0 {
		return 0, nil
	}
	return scale(total), nil
}

// CountMessages returns the scaled token count of a conversation history in the
// backend-neutral form used by session storage.
func CountMessages(msgs []agentapi.Message) (int, error) {
	var texts []string
	images := 0
	for _, m := range msgs {
		texts = append(texts, m.Content)
		images += len(m.Images)
	}
	n, err := CountAll(texts...)
	if err != nil {
		return 0, err
	}
	return n + images*tokensPerImage, nil
}

// CountSchemaMessages returns the scaled token count of the messages an engine
// is about to send, including the tool schemas that accompany every call.
//
// Tool schemas are counted because adk re-sends them on every model call, so
// they consume the window on every iteration rather than once.
func CountSchemaMessages(msgs []*schema.Message, tools []*schema.ToolInfo) (int, error) {
	var texts []string
	images := 0
	for _, m := range msgs {
		if m == nil {
			continue
		}
		texts = append(texts, m.ToolName, m.ToolCallID)
		for _, tc := range m.ToolCalls {
			texts = append(texts, tc.Function.Name, tc.Function.Arguments)
		}
		// A multimodal message repeats Content as a text part, so counting
		// both would double it. Count the parts, and charge images flat.
		if len(m.UserInputMultiContent) > 0 {
			for _, part := range m.UserInputMultiContent {
				switch part.Type {
				case schema.ChatMessagePartTypeText:
					texts = append(texts, part.Text)
				case schema.ChatMessagePartTypeImageURL:
					images++
				}
			}
			continue
		}
		texts = append(texts, m.Content)
	}

	n, err := CountAll(texts...)
	if err != nil {
		return 0, err
	}
	n += images * tokensPerImage

	for _, t := range tools {
		if t == nil {
			continue
		}
		tn, err := CountAll(t.Name, t.Desc)
		if err != nil {
			return 0, err
		}
		n += tn
		// Parameters carry the bulk of a tool schema; ToJSONSchema renders the
		// exact shape the model receives.
		if p, err := t.ToJSONSchema(); err == nil && p != nil {
			raw, err := json.Marshal(p)
			if err != nil {
				continue
			}
			pn, err := Count(string(raw))
			if err != nil {
				return 0, err
			}
			n += pn
		}
	}
	return n, nil
}
