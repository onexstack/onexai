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

// Package imagegen draws images and stores them like any other image.
//
// # Why generation goes through the asset store
//
// A drawn image is an image: it needs a name, an owner, a size check, a place
// to live and a lifecycle, and every one of those already exists for an
// uploaded picture. Storing it beside those means the code that resolves an
// image for a model, the code that serves one to a client, and the code that
// will eventually expire one all work on it unchanged — and it means a
// generated picture is not a second kind of thing that every reader has to
// learn about.
package imagegen

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/agent/biz/v1/asset"
	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// ProviderSet defines Wire DI providers for the image-generation biz layer.
var ProviderSet = wire.NewSet(New, wire.Bind(new(ImageGenBiz), new(*imageGenBiz)))

// ImageGenBiz draws images.
type ImageGenBiz interface {
	// Generate draws the requested images, stores them, and returns them as
	// references.
	Generate(ctx context.Context, req *v1.ImageGenerationRequest) (*v1.ImageGenerationResponse, error)
	// Configured reports whether an image model is wired up. A deployment
	// without one answers every other request and refuses a drawing with a
	// message naming the missing configuration.
	Configured() bool
}

var _ ImageGenBiz = (*imageGenBiz)(nil)

// ErrNotConfigured is returned when no image model is configured.
var ErrNotConfigured = errors.New("image generation is not configured")

// imageGenBiz implements ImageGenBiz.
type imageGenBiz struct {
	generator chatmodel.ImageGenerator
	assets    asset.AssetBiz
}

// New creates an imageGenBiz. A nil generator yields one that reports the
// missing configuration, which is a deployment's decision rather than a fault.
func New(generator chatmodel.ImageGenerator, assets asset.AssetBiz) *imageGenBiz {
	return &imageGenBiz{generator: generator, assets: assets}
}

// Configured implements ImageGenBiz.
func (b *imageGenBiz) Configured() bool {
	return b != nil && b.generator != nil && b.assets != nil && b.assets.Configured()
}

// Generate implements ImageGenBiz.
func (b *imageGenBiz) Generate(ctx context.Context, req *v1.ImageGenerationRequest) (*v1.ImageGenerationResponse, error) {
	if !b.Configured() {
		return nil, ErrNotConfigured
	}

	drawn, err := b.generator.Generate(ctx, chatmodel.ImageRequest{
		Prompt:  req.GetPrompt(),
		Model:   req.GetModel(),
		Size:    req.GetSize(),
		Quality: req.GetQuality(),
		N:       int(req.GetN()),
	})
	if err != nil {
		return nil, err
	}

	// Storing is what turns a produced image into one this service serves: the
	// provider's answer is bytes, and a caller handed bytes has to solve
	// storage, naming and expiry for itself.
	ownerID := req.GetSenderID()
	out := make([]*v1.GeneratedImage, 0, len(drawn))
	for i, img := range drawn {
		ref, err := b.assets.Store(ctx, media.KindGenerated, ownerID, generatedName(i, img.MimeType),
			media.SourceGenerated, img.Data)
		if err != nil {
			// A batch that stored three of five is reported as three, with the
			// failure logged: the alternative is refusing every image because
			// one could not be kept, which loses work that was already paid for
			// at the provider.
			slog.WarnContext(ctx, "could not store a generated image", "index", i, "error", err)
			continue
		}
		out = append(out, &v1.GeneratedImage{
			Url:           ref.URL,
			AssetId:       ref.AssetID,
			RevisedPrompt: img.RevisedPrompt,
			Ref:           agentapi.RefToProto(ref),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("image generation: none of the %d produced images could be stored", len(drawn))
	}

	if isB64Requested(req.GetResponseFormat()) {
		// The bytes are read back for the callers that asked for them inline.
		// They are read from the store rather than kept from the provider so
		// that the two answers cannot differ, and so an image that was stored
		// under a different owner still comes back the same.
		for i, g := range out {
			data, _, err := b.assets.Content(ctx, g.GetAssetId())
			if err != nil {
				slog.WarnContext(ctx, "could not re-read a generated image for an inline answer",
					"asset_id", g.GetAssetId(), "error", err)
				continue
			}
			out[i].B64Json = base64.StdEncoding.EncodeToString(data)
		}
	}

	return &v1.ImageGenerationResponse{
		Created: time.Now().Unix(),
		Data:    out,
	}, nil
}

// isB64Requested reports whether the caller asked for inline bytes.
//
// OpenAI's spelling is "b64_json"; "base64" is accepted too because it is what
// a reader who did not consult the contract writes, and refusing it would fail
// a request whose intent is unmistakable.
func isB64Requested(format string) bool {
	switch format {
	case "b64_json", "base64":
		return true
	default:
		return false
	}
}

// generatedName names a stored image, since a drawing arrives with no filename.
func generatedName(index int, mimeType string) string {
	return fmt.Sprintf("generated-%d%s", index+1, media.ExtensionFor(mimeType))
}
