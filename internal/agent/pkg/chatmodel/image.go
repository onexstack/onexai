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

package chatmodel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/onexstack/onexai/internal/agent/pkg/clientset/typed/image"
)

// ImageRequest is one request to draw something.
type ImageRequest struct {
	// Prompt is what to draw.
	Prompt string
	// Model overrides the generator's configured model.
	Model string
	// Size is the requested dimensions, e.g. "1024x1024". Empty uses the
	// provider's default.
	Size string
	// Quality is the provider's quality knob, passed through uninterpreted.
	Quality string
	// N is how many images to produce. Zero means one.
	N int
}

// GeneratedImage is one produced image.
type GeneratedImage struct {
	// Data is the image's bytes.
	Data []byte
	// MimeType is what the bytes are. It is sniffed rather than trusted: a
	// provider that answers a PNG under an image/jpeg content type would
	// otherwise be recorded wrongly, and the type is what the model edge and
	// the store both key off.
	MimeType string
	// RevisedPrompt is the provider's rewrite of the prompt, when it gives one.
	RevisedPrompt string
}

// ImageGenerator draws images from prompts.
type ImageGenerator interface {
	// Generate draws the requested images, or reports why it could not.
	Generate(ctx context.Context, req ImageRequest) ([]GeneratedImage, error)
}

// OpenAIImageGenerator calls an OpenAI-compatible /images/generations endpoint.
//
// # Why it asks for base64
//
// The OpenAI contract offers two answers: a temporary URL, or the bytes inline.
// The URL expires — often within the hour — and this service stores what comes
// back and serves it afterwards, so a URL that has already stopped working is
// the one failure mode that would be invisible at generation time and obvious
// much later. Asking for the bytes removes the second request and the expiry
// with it.
type OpenAIImageGenerator struct {
	model  string
	client *image.Client
}

// NewOpenAIImageGenerator builds a generator. A blank model is refused at
// Generate time rather than here, so a deployment that sets the model from a
// row at request time can still construct the client at startup.
func NewOpenAIImageGenerator(baseURL, apiKey, model string) *OpenAIImageGenerator {
	return &OpenAIImageGenerator{
		model:  model,
		client: image.NewForConfig(baseURL, apiKey),
	}
}

// Name identifies the generator, for a log line.
func (g *OpenAIImageGenerator) Name() string { return "openai-image" }

// Generate draws the images.
func (g *OpenAIImageGenerator) Generate(ctx context.Context, req ImageRequest) ([]GeneratedImage, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("image generation: no prompt")
	}
	model := req.Model
	if model == "" {
		model = g.model
	}
	if model == "" {
		return nil, fmt.Errorf("image generation: no model configured")
	}
	n := req.N
	if n <= 0 {
		n = 1
	}

	// The call is not retried, unlike the chat completions this service sends
	// elsewhere; see the retry note on image.NewForConfig.
	payload, _, err := g.client.Generate(ctx, image.Request{
		Model:          model,
		Prompt:         req.Prompt,
		N:              n,
		ResponseFormat: "b64_json",
		Size:           req.Size,
		Quality:        req.Quality,
	})
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("image generation: decode response: %w", err)
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("image generation: provider returned no images")
	}

	out := make([]GeneratedImage, 0, len(parsed.Data))
	for i, item := range parsed.Data {
		if item.B64JSON == "" {
			// A provider that ignored the requested format answered with a URL.
			// Saying so beats returning an empty image: the operator can then
			// fix the provider rather than wonder where the picture went.
			return nil, fmt.Errorf("image generation: image %d came back as a URL, not inline data", i)
		}
		data, err := decodeBase64(item.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("image generation: decode image: %w", err)
		}
		out = append(out, GeneratedImage{
			Data:          data,
			MimeType:      sniffImageMime(data),
			RevisedPrompt: item.RevisedPrompt,
		})
	}
	return out, nil
}

// decodeBase64 accepts both alphabets, for the same reason media.DecodeDataURL
// does: some providers pad, some do not, and refusing one of them would reject
// an image that arrived intact.
func decodeBase64(s string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// sniffImageMime reports what the bytes are.
//
// It duplicates the media package's sniffer rather than importing it: this
// package is the model-vendor seam and has no business depending on the store.
// The two agree because both call net/http's detector on the leading bytes.
func sniffImageMime(data []byte) string {
	const sniffLen = 512
	if len(data) > sniffLen {
		data = data[:sniffLen]
	}
	ct := strings.ToLower(strings.TrimSpace(http.DetectContentType(data)))
	if semi := strings.IndexByte(ct, ';'); semi >= 0 {
		ct = strings.TrimSpace(ct[:semi])
	}
	if strings.HasPrefix(ct, "image/") {
		return ct
	}
	// An unknown binary is still a picture the provider drew; calling it PNG is
	// wrong but harmless, while calling it octet-stream would have the model
	// edge produce a data URL some endpoints reject.
	return "image/png"
}

// truncate shortens a provider's error body for a log line.
