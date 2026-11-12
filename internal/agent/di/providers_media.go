// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package di

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/biz/v1/imagegen"
	"github.com/onexstack/onexai/internal/agent/biz/v1/mediagc"
	"github.com/onexstack/onexai/internal/agent/handler"
	"github.com/onexstack/onexai/internal/agent/pkg/agentbuilder"
	"github.com/onexstack/onexai/internal/agent/pkg/agentedge"
	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/agent/pkg/interactionlog"
	"github.com/onexstack/onexai/internal/agent/pkg/kbtool"
	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"

	"github.com/onexstack/onexai/internal/pkg/netguard"

	"github.com/onexstack/onexai/internal/agent/store"
	sqlstore "github.com/onexstack/onexai/internal/agent/store/sqlstore"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// ProvideMediaBundle builds the image store from the media configuration.
//
// A disabled driver is not an error: a deployment that has not decided where
// its images live still answers every text request, and refusing an image names
// the missing configuration rather than failing somewhere deeper. The bundle is
// returned unconfigured in that case, and every caller checks.
func ProvideMediaBundle(cfg *Config) (*media.Bundle, error) {
	opts := cfg.MediaOptions
	if !opts.Enabled() {
		slog.Info("media storage is disabled: images cannot be stored or read",
			"hint", "set media.driver to \"local\" or \"cos\" to keep them")
		return media.NewBundle(nil, nil), nil
	}

	// Which driver, and where it writes, are the two facts a deployment gets
	// wrong — a destination that resolves somewhere unexpected, and a driver
	// left unset so nothing is stored at all — so both are logged at startup.
	// The destination key differs per driver, which is why the store reports it
	// rather than this function guessing: a directory and a bucket do not have
	// the same useful second half.
	store, err := media.NewStore(opts.StoreConfig())
	if err != nil {
		return nil, err
	}
	attrs := append([]any{"driver", store.Name()}, destinationOf(store)...)
	slog.Info("media storage configured", attrs...)
	return media.NewBundle(store, nil), nil
}

// destinationOf asks a store where it writes, for the startup log line above.
// A driver that has nothing to add contributes nothing.
func destinationOf(store media.Store) []any {
	if d, ok := store.(media.Destination); ok {
		return d.Where()
	}
	return nil
}

// ProvideImageGenerator builds the drawing client, or nil when no image model
// is configured.
//
// Drawing is off by default rather than pointed at a guess: an image model is a
// different endpoint from the chat one in every deployment that has one, and
// sending a prompt to whatever model happens to be configured for chat would
// fail at best and bill for a drawing at worst.
func ProvideImageGenerator(cfg *Config) chatmodel.ImageGenerator {
	opts := cfg.AgentOptions
	if opts.ImageModel == nil || !opts.ImageModel.Enabled() {
		return nil
	}
	inherited := chatmodel.Inherit(*opts.ImageModel, customoptions.ChatModelConfig{
		BaseURL: cfg.LLMBaseURL,
		APIKey:  cfg.LLMAPIKey,
	})
	gen := chatmodel.NewOpenAIImageGenerator(inherited.BaseURL, inherited.APIKey, inherited.Model)
	slog.Info("image generation configured", "model", inherited.Model, "generator", gen.Name())
	return gen
}

// ProvideImageDrawer adapts the image-generation service to the one method the
// agent builder's drawing tool needs.
//
// The adapter exists because the builder sits below the biz layer: it cannot
// import the service, and the service answers with the API's proto type while
// the tool wants a reference. Both conversions live here, at the seam, rather
// than in either of them.
func ProvideImageDrawer(gen imagegen.ImageGenBiz) agentbuilder.ImageGenerator {
	if !gen.Configured() {
		return nil
	}
	return &imageDrawer{gen: gen}
}

// imageDrawer is the adapter.
type imageDrawer struct {
	gen imagegen.ImageGenBiz
}

var _ agentbuilder.ImageGenerator = (*imageDrawer)(nil)

// Draw implements agentbuilder.ImageGenerator.
//
// # The asker travels on the context
//
// A drawn image is stored under whoever asked for it, because that identity is
// what the row is keyed by and what a later read is checked against. The tool
// that calls this has no argument for it — the model supplies a prompt and
// nothing else — so it comes off the run's context, where the biz put it for
// exactly this reason.
//
// Leaving it unset is not a harmless default: the store falls back to one
// shared `anon` directory, so every user's drawings land together and none of
// them is attributable.
func (d *imageDrawer) Draw(ctx context.Context, prompt string) (media.Ref, error) {
	out, err := d.gen.Generate(ctx, &v1.ImageGenerationRequest{
		Prompt:   prompt,
		SenderID: contextx.UserID(ctx),
	})
	if err != nil {
		return media.Ref{}, err
	}
	data := out.GetData()
	if len(data) == 0 {
		return media.Ref{}, errors.New("image generation returned no images")
	}
	ref := data[0].GetRef()
	r, ok := agentapi.RefFromProto(ref)
	if !ok {
		// A nil reference and one that names nothing are the same fault here:
		// the drawing came back without a way to reach it.
		return media.Ref{}, errors.New("image generation returned no reference")
	}
	return r, nil
}

// ProvideAssetRetention is how long a newly stored image is kept, as the asset
// biz reads it.
//
// It is a provider rather than a field the biz reaches for so the biz stays
// ignorant of where the number comes from — and so a test can hand it a
// duration without a config file.
func ProvideAssetRetention(cfg *Config) time.Duration {
	return cfg.MediaOptions.Retention
}

// ProvideMediaCollector builds the expired-image collector, or nil when no
// retention has been set.
//
// It is off by default and that is the whole point: the retention decides how
// long a user's screenshot stays readable in a conversation they can still
// scroll back to, which is a policy rather than a default this code should
// choose.
func ProvideMediaCollector(cfg *Config, rows store.AssetStore, bundle *media.Bundle) mediagc.Collector {
	if !cfg.MediaOptions.GCEnabled {
		return nil
	}
	opt := cfg.MediaOptions
	return mediagc.New(rows, bundle, slog.Default(), mediagc.Options{
		Enabled:  true,
		Interval: opt.GCInterval,
		Batch:    opt.GCBatch,
	})
}

// ProvideAssetStore builds the asset row store. With no database it returns
// the no-op store, so an upload fails loudly instead of appearing to persist.
func ProvideAssetStore(db *gorm.DB) store.AssetStore {
	if db == nil {
		return store.NewEmptyStore().Assets()
	}
	return sqlstore.NewStore(db).Assets()
}

// ProvideImageInliner builds the model-edge resolver.
//
// The fetcher is built only when the deployment allows reaching a URL at all,
// and it is configured from the same options block as the store so that a
// deployment which has thought about its images has thought about both.
func ProvideImageInliner(
	bundle *media.Bundle, rows store.AssetStore, cfg *Config, recorder *metrics.Metrics,
) *agentedge.Inliner {
	deps := agentedge.Deps{
		Bundle:    bundle,
		Rows:      rows,
		Metrics:   recorder,
		MaxPixels: cfg.AgentOptions.MaxImagePixels,
		Fetcher: netguard.NewFetcher(netguard.Options{
			AllowPrivateHosts: cfg.MediaOptions.AllowPrivateHosts,
			MaxBytes:          cfg.MediaOptions.MaxFetchBytes,
		}),
	}
	return agentedge.New(deps)
}

// retrievalAuditStore builds the audit writer's store from the database, not
// from the cached resource aggregate: the audit table is written whenever a
// database exists, even in a deployment that has disabled the AI-resource
// store (store.source=""), and routing it through AIStore would silently drop
// those writes.
func retrievalAuditStore(db *gorm.DB) store.RetrievalAuditStore {
	if db == nil {
		return nil
	}
	return sqlstore.NewStore(db).RetrievalAudit()
}

// ProvideRetrievalAudit builds the retrieval audit sink, or nil when there is no
// database to write to.
func ProvideRetrievalAudit(db *gorm.DB) kbtool.AuditFunc {
	return newRetrievalAudit(retrievalAuditStore(db))
}

// ProvideInteractionWriter builds the audit recorder, or nil when there is no
// database to write to. It is a separate provider so the agent service can be
// assembled without an audit store — the feature is optional, and its absence
// must not make the service unbuildable.
func ProvideInteractionWriter(db *gorm.DB) *interactionlog.Writer {
	if db == nil {
		return nil
	}
	return interactionlog.New(sqlstore.NewStore(db).Interactions())
}

// ProvideMediaHandler is the local store the static media route serves from, or
// nil for an object store, which serves its own bytes. Asking the store rather
// than reading a config flag means the two cannot disagree.
func ProvideMediaHandler(bundle *media.Bundle) handler.MediaHandler {
	if local, ok := bundle.Store().(*media.LocalStore); ok {
		return local
	}
	return nil
}
