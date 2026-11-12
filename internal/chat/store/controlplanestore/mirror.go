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

package controlplanestore

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/chat/model"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
	"github.com/onexstack/onexai/pkg/declclient/declstore"
	"github.com/onexstack/onexai/pkg/declclient/mirrorstore"
	"github.com/onexstack/onexai/pkg/options"
)

// MirrorConfig points a subset of the chat store's Lists at the PostgreSQL
// projection.
//
// Only List is redirected; Get keeps reading the control plane, where it is
// already O(1) and where read-your-own-write matters.
type MirrorConfig struct {
	DB      *gorm.DB
	Options *options.StoreOptions
	// Namespace is the scope the store was built for; a mirror-full Get reads the
	// API server directly and needs it.
	Namespace string
}

// attach installs the projection-backed List on every chat kind configured for
// it.
func (m *MirrorConfig) attach(s *Store, client *declclient.Client) error {
	if m == nil || m.DB == nil || m.Options == nil {
		return nil
	}

	dropInformer(m, &s.peerAgents.Store.Reader, "PeerBinding", func(ctx context.Context, name string) (model.PeerAgentM, error) {
		item, err := client.Clientset.ChatV1alpha1().PeerBindings(m.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return model.PeerAgentM{}, err
		}
		return peerBindingFromCR(item), nil
	})

	if err := attachKind(m, &s.peerAgents.Store.Reader, "PeerBinding", peerBindingFromRaw); err != nil {
		return err
	}
	return nil
}

// dropInformer switches a kind's Get to the API server behind a bounded cache.
//
// A mirror-full kind holds no rows in this process, so a Get that missed the
// cache must ask the API server rather than scan an empty indexer and call the
// object missing.
func dropInformer[M any](
	cfg *MirrorConfig, reader *declstore.Reader[M], kind string, get func(context.Context, string) (M, error),
) {
	if cfg.Options.BackendFor(kind) != options.BackendMirrorFull {
		return
	}
	reader.NoScan = true
	reader.GetByKey = declstore.APIGetByKey(declstore.NewGetCache[M](cfg.Options.GetCacheSize), get)
}

// attachKind wires one kind, if the configuration selects a mirror backend.
func attachKind[M any](
	cfg *MirrorConfig,
	reader *declstore.Reader[M],
	kind string,
	decode func(context.Context, mirrorstore.RawRow) (M, error),
) error {
	switch cfg.Options.BackendFor(kind) {
	case options.BackendMirrorList, options.BackendMirrorFull:
	default:
		return nil
	}

	mirror, err := mirrorstore.New(mirrorstore.Config{
		DB:        cfg.DB,
		Schema:    cfg.Options.Mirror.Schema,
		Group:     chatv1alpha1.GroupName,
		Kind:      kind,
		MaxLag:    cfg.Options.Mirror.MaxLag,
		CountMode: mirrorstore.CountMode(cfg.Options.Mirror.CountMode),
	}, decode)
	if err != nil {
		return fmt.Errorf("controlplanestore: %s reads from the projection: %w", kind, err)
	}
	reader.ListPage = mirror.ListPage
	return nil
}

// The decoders rebuild the control-plane object and hand it to the same
// conversion the lister path uses.

func peerBindingFromRaw(_ context.Context, raw mirrorstore.RawRow) (model.PeerAgentM, error) {
	cr := &chatv1alpha1.PeerBinding{ObjectMeta: mirrorstore.Meta(raw)}
	if err := mirrorstore.Unmarshal(raw, &cr.Spec, &cr.Status); err != nil {
		return model.PeerAgentM{}, err
	}
	return peerBindingFromCR(cr), nil
}
