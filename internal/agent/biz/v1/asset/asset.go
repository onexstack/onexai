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

// Package asset implements the media asset business logic: storing an uploaded
// image and answering for it afterwards.
package asset

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/wire"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// ProviderSet defines Wire DI providers for the asset biz layer.
var ProviderSet = wire.NewSet(New, wire.Bind(new(AssetBiz), new(*assetBiz)))

// AssetBiz is the media asset business logic.
type AssetBiz interface {
	// Create stores an uploaded image and returns the asset that names it.
	Create(ctx context.Context, req *v1.CreateAssetRequest) (*v1.Asset, error)
	// Store keeps image bytes this service obtained without an upload — a
	// legacy inline image pulled out of a session, or one a model generated —
	// and returns the reference that names them.
	//
	// It is the same work Create does once the base64 is decoded, exposed
	// separately so those callers need not re-encode bytes they already hold as
	// bytes. The ownership and dedup rules are the same; only the entry point
	// differs.
	Store(ctx context.Context, kind, ownerID, filename, source string, data []byte) (media.Ref, error)
	// Get returns an asset's metadata.
	Get(ctx context.Context, assetID string) (*v1.Asset, error)
	// Content returns an asset's bytes and their media type.
	//
	// It exists so a caller that needs the image itself — another service
	// forwarding it somewhere else, a console rendering it — does not have to
	// know which driver is configured or where the object store's bytes are
	// reachable from. The service proxies: one hop, one answer, whichever
	// driver is behind it.
	Content(ctx context.Context, assetID string) ([]byte, string, error)
	// Delete removes an asset and its bytes.
	Delete(ctx context.Context, assetID string) error
	// Configured reports whether a media store is wired up. A deployment with
	// no store answers every text request and refuses an image with a message
	// that names the configuration, rather than failing somewhere deeper.
	Configured() bool
}

var _ AssetBiz = (*assetBiz)(nil)

// assetBiz implements AssetBiz.
type assetBiz struct {
	bundle  *media.Bundle
	store   agentstore.AssetStore
	metrics *metrics.Metrics
	// retention, when positive, is written into every new row as a deadline.
	// Zero means the row has no expiry, which is what the collector reads as
	// "never".
	retention time.Duration
}

// New creates an assetBiz. A nil metrics recorder is a no-op, so the biz works
// the same in a test as in a deployment.
func New(bundle *media.Bundle, store agentstore.AssetStore, recorder *metrics.Metrics, retention time.Duration) *assetBiz {
	return &assetBiz{bundle: bundle, store: store, metrics: recorder, retention: retention}
}

// ErrNotConfigured is returned when a deployment has no media store.
//
// It is media's own sentinel rather than a second one saying the same thing:
// "this deployment has nowhere to put bytes" is one condition, and two errors
// for it would make `errors.Is` answer differently depending on which layer the
// caller happened to check. The remedy is the operator's — nothing is broken,
// and they have to decide where images should live.
var ErrNotConfigured = media.ErrNotConfigured

// ErrEmptyData is returned when an upload carries no bytes.
var ErrEmptyData = errors.New("no image data was uploaded")

// Configured implements AssetBiz.
func (b *assetBiz) Configured() bool { return b != nil && b.bundle.Configured() }

// Create implements AssetBiz.
func (b *assetBiz) Create(ctx context.Context, req *v1.CreateAssetRequest) (*v1.Asset, error) {
	if !b.Configured() {
		return nil, ErrNotConfigured
	}
	data, err := decodeData(req.GetData())
	if err != nil {
		return nil, err
	}
	ref, err := b.Store(ctx, req.GetKind(), req.GetOwnerID(), req.GetFilename(), req.GetSource(), data)
	if err != nil {
		return nil, err
	}
	row, err := b.store.Get(ctx, where.F("id", ref.AssetID))
	if err != nil {
		return nil, err
	}
	return assetToProto(row), nil
}

// Store implements AssetBiz.
func (b *assetBiz) Store(
	ctx context.Context, kind, ownerID, filename, source string, data []byte,
) (media.Ref, error) {
	if !b.Configured() {
		return media.Ref{}, ErrNotConfigured
	}
	if len(data) == 0 {
		return media.Ref{}, ErrEmptyData
	}

	if strings.TrimSpace(kind) == "" {
		kind = media.KindImage
	}
	if source == "" {
		source = media.SourceUpload
	}
	digest := sha256.Sum256(data)
	digestHex := hex.EncodeToString(digest[:])

	// A file this owner has already stored is answered with the row that
	// exists rather than written again. The check is by owner as well as
	// digest: two people who happen to upload the same picture must not be
	// shown each other's asset, however identical the bytes.
	if existing, err := b.store.GetByDigest(ctx, ownerID, digestHex); err == nil {
		return refFromRow(existing), nil
	} else if !errors.Is(err, agentstore.ErrNotFound) {
		// The lookup failed for a reason other than absence — a database that
		// is down, say. Storing the bytes again is the better degradation than
		// refusing: a duplicate costs storage, and refusing costs the caller an
		// image, for a cause nothing in this request can act on.
		logDedupLookupFailed(ctx, digestHex, err)
	}

	ref, err := b.bundle.Put(ctx, kind, ownerID, filename, data)
	if err != nil {
		// A refusal is recorded as well as a success: a run of failures here is
		// what a misconfigured driver or a wrong content type looks like from
		// the outside, before anyone reports an image that would not send.
		b.metrics.ObserveImage("upload", "failed", 0)
		return media.Ref{}, err
	}
	b.metrics.ObserveImage("upload", "ok", ref.Size)

	row := &model.AssetM{
		ID:        ref.AssetID,
		OwnerID:   ownerID,
		Kind:      kind,
		ObjectKey: ref.ObjectKey,
		URL:       ref.URL,
		MimeType:  ref.MimeType,
		Size:      ref.Size,
		Width:     ref.Width,
		Height:    ref.Height,
		Name:      ref.Name,
		Sha256:    digestHex,
		Source:    source,
	}
	if b.retention > 0 {
		// Stamped at store time rather than computed by the collector, so a
		// later change to the retention applies to what comes next instead of
		// reaching back and expiring images that were kept under the old one.
		deadline := time.Now().Add(b.retention)
		row.ExpiresAt = &deadline
	}
	if err := b.store.Create(ctx, row); err != nil {
		// The bytes are removed with the row that failed to describe them. A
		// file nothing points at is a leak that only a sweep would find, and
		// the sweep looks at rows.
		if delErr := b.bundle.Delete(ctx, ref); delErr != nil {
			return media.Ref{}, fmt.Errorf("%w (and the orphaned bytes could not be removed: %v)", err, delErr)
		}
		return media.Ref{}, err
	}
	return refFromRow(row), nil
}

// Get implements AssetBiz.
func (b *assetBiz) Get(ctx context.Context, assetID string) (*v1.Asset, error) {
	row, err := b.store.Get(ctx, where.F("id", assetID))
	if err != nil {
		return nil, err
	}
	return assetToProto(row), nil
}

// Content implements AssetBiz.
func (b *assetBiz) Content(ctx context.Context, assetID string) ([]byte, string, error) {
	if !b.Configured() {
		return nil, "", ErrNotConfigured
	}
	row, err := b.store.Get(ctx, where.F("id", assetID))
	if err != nil {
		return nil, "", err
	}
	return b.bundle.OpenBytes(ctx, media.Ref{ObjectKey: row.ObjectKey, MimeType: row.MimeType})
}

// Delete implements AssetBiz.
//
// The row is read before the bytes are removed, because the key the bytes are
// at is only in the row: deleting the object first and the row second would
// lose the address of anything the second step failed on.
func (b *assetBiz) Delete(ctx context.Context, assetID string) error {
	row, err := b.store.Get(ctx, where.F("id", assetID))
	if err != nil {
		return err
	}
	if err := b.bundle.Delete(ctx, media.Ref{ObjectKey: row.ObjectKey}); err != nil {
		return err
	}
	return b.store.Delete(ctx, where.F("id", assetID))
}

// logDedupLookupFailed records a duplicate check that could not be answered.
//
// It is a warning rather than an error on purpose: the upload proceeds, so the
// request did what the caller asked. Leaving a trace is what makes a database
// that has been down for a while recognisable in the logs as a growing pile of
// duplicate objects, rather than as silence.
func logDedupLookupFailed(ctx context.Context, digest string, err error) {
	slog.WarnContext(ctx, "media: duplicate check failed; storing the image again", "digest", digest, "error", err)
}

// decodeData reads the base64 payload of an upload.
//
// Both the padded and the unpadded standard encodings are accepted. The
// difference is a client detail — some encoders strip the trailing "=" — and
// refusing one of them would reject an image that arrived intact.
func decodeData(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, ErrEmptyData
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("media: decode image data: %w", err)
	}
	if len(data) == 0 {
		return nil, ErrEmptyData
	}
	return data, nil
}

// refFromRow turns a stored row back into the reference that names it.
//
// The object key is included, so a caller that can reach the store directly
// resolves the bytes without a second read of this table.
func refFromRow(row *model.AssetM) media.Ref {
	if row == nil {
		return media.Ref{}
	}
	return media.Ref{
		AssetID:   row.ID,
		ObjectKey: row.ObjectKey,
		URL:       row.URL,
		MimeType:  row.MimeType,
		Size:      row.Size,
		Width:     row.Width,
		Height:    row.Height,
		Name:      row.Name,
		Source:    row.Source,
	}
}

// assetToProto renders a row as the API type.
func assetToProto(row *model.AssetM) *v1.Asset {
	if row == nil {
		return nil
	}
	out := &v1.Asset{
		AssetId:   row.ID,
		Kind:      row.Kind,
		Url:       row.URL,
		MimeType:  row.MimeType,
		Size:      row.Size,
		Width:     int32(row.Width),
		Height:    int32(row.Height),
		Name:      row.Name,
		Source:    row.Source,
		CreatedAt: row.CreatedAt.Unix(),
	}
	if row.ExpiresAt != nil {
		out.ExpiresAt = row.ExpiresAt.Unix()
	}
	return out
}
