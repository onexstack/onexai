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
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/pkg/declcrud"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
	"github.com/onexstack/onexai/pkg/declclient/declstore"
)

// PeerBindingStore adds the triple-keyed lookups the binding paths use.
//
// The object's name is derived from (backend, scope, peerID) rather than
// stored, because a peer id is not a DNS name (Lark open ids contain
// underscores) and a name derived from the triple is what makes Get and Delete
// single-object operations instead of scans.
type PeerBindingStore struct {
	*declstore.Store[model.PeerAgentM]
}

// BindingName returns the object name for a binding triple. Exported so a
// caller can address the object directly (kubectl, the console).
func BindingName(backend, scope, peerID string) string {
	sum := sha256.Sum256([]byte(backend + "\x00" + scope + "\x00" + peerID))
	return "pb-" + hex.EncodeToString(sum[:])[:16]
}

// resolvePeerBindingNamespace reports the namespace the named binding lives in,
// from the cluster-wide cache; fallback is used when the cache does not hold it.
//
// The store is built with the configured control-plane namespace, which a
// deployment that reads every namespace leaves empty. A namespaced resource has
// no cluster-wide object URL: a Get/Update/Delete issued with an empty namespace
// is answered with 405/404, which used to surface only as a logged warning after
// the account binding had already been written.
func resolvePeerBindingNamespace(
	list func() ([]*chatv1alpha1.PeerBinding, error), name, fallback string,
) (string, error) {
	items, err := list()
	if err != nil {
		return fallback, nil
	}
	var namespaces []string
	for _, item := range items {
		if item.Name == name {
			namespaces = append(namespaces, item.Namespace)
		}
	}
	switch len(namespaces) {
	case 0:
		return fallback, nil
	case 1:
		return namespaces[0], nil
	default:
		sort.Strings(namespaces)
		return "", &declstore.AmbiguousKeyError{Key: name, Namespaces: dedupeSorted(namespaces)}
	}
}

// dedupeSorted removes adjacent duplicates from a sorted namespace list.
func dedupeSorted(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

// peerBindingWriteNamespace is where a binding with no namespace of its own is
// written: the configured namespace, else the default namespace, which is where
// the platform's objects live in a deployment that watches every namespace.
func peerBindingWriteNamespace(configured string) string {
	if configured != "" {
		return configured
	}
	return metav1.NamespaceDefault
}

// GetByPeer returns the binding for one peer, or (nil, nil) when there is none:
// "this peer has no binding" is the ordinary state for everyone who is not a
// OneX user.
func (s *PeerBindingStore) GetByPeer(ctx context.Context, backend, scope, peerID string) (*model.PeerAgentM, error) {
	row, err := s.Get(ctx, where.F("name", BindingName(backend, scope, peerID)))
	if err != nil {
		return nil, err
	}
	return row, nil
}

// Put upserts a binding by its triple.
func (s *PeerBindingStore) Put(ctx context.Context, row *model.PeerAgentM) error {
	if err := s.Update(ctx, row); err != nil {
		if apierrors.IsNotFound(err) {
			return s.Create(ctx, row)
		}
		return err
	}
	return nil
}

// DeleteByPeer removes a binding. Deleting one that is not there is not an
// error: unbinding twice is the same outcome as unbinding once.
func (s *PeerBindingStore) DeleteByPeer(ctx context.Context, backend, scope, peerID string) error {
	err := s.Delete(ctx, where.F("name", BindingName(backend, scope, peerID)))
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// NewPeerBindingStore builds the peer-binding accessor.
func NewPeerBindingStore(client *declclient.Client, namespace string) *PeerBindingStore {
	lister := client.PeerBindings()
	list := func() ([]*chatv1alpha1.PeerBinding, error) {
		return lister.List(labels.Everything())
	}

	return &PeerBindingStore{Store: declcrud.NewStore(declcrud.Spec[model.PeerAgentM, *chatv1alpha1.PeerBinding]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*chatv1alpha1.PeerBinding] {
			return client.Clientset.ChatV1alpha1().PeerBindings(namespace)
		},
		List: list,
		FromCR: func(_ context.Context, cr *chatv1alpha1.PeerBinding) (model.PeerAgentM, error) {
			return peerBindingFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.PeerAgentM, cr *chatv1alpha1.PeerBinding) error {
			PeerBindingToCR(row, cr)
			return nil
		},
		New: func(name, namespace string) *chatv1alpha1.PeerBinding {
			return &chatv1alpha1.PeerBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key: "name",
		// The row has no name column: its identity is the triple, and the
		// derived object name is what the store keys on.
		KeyOf:  func(row model.PeerAgentM) string { return BindingName(row.Backend, row.Scope, row.PeerID) },
		NameOf: func(row model.PeerAgentM) string { return BindingName(row.Backend, row.Scope, row.PeerID) },
		Namespaces: declcrud.Namespaces[model.PeerAgentM]{
			Create: func(model.PeerAgentM) string { return peerBindingWriteNamespace(namespace) },
			Update: func(model.PeerAgentM) string { return peerBindingWriteNamespace(namespace) },
			Delete: func() string { return peerBindingWriteNamespace(namespace) },
			Resolve: func(key, fallback string) (string, error) {
				return resolvePeerBindingNamespace(list, key, fallback)
			},
		},
		Column: func(row model.PeerAgentM, name string) (string, bool) {
			switch name {
			case "name":
				return BindingName(row.Backend, row.Scope, row.PeerID), true
			case "backend":
				return row.Backend, true
			case "scope":
				return row.Scope, true
			case "peer_id":
				return row.PeerID, true
			case "canonical":
				return row.Canonical, true
			case "source":
				return row.Source, true
			default:
				return "", false
			}
		},
	})}
}

// annotationUpdatedAt carries the row's last write time on the object.
//
// The spec has no timestamp, and CreationTimestamp is only the first write, so
// without this annotation every read of a binding reports the moment it was
// created rather than last changed. An annotation survives the round trip
// without changing the CRD schema or regenerating deepcopy.
const annotationUpdatedAt = "onexai.io/updated-at"

func peerBindingFromCR(cr *chatv1alpha1.PeerBinding) model.PeerAgentM {
	updatedAt := cr.CreationTimestamp.Time
	if raw := cr.Annotations[annotationUpdatedAt]; raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			updatedAt = parsed
		}
	}
	return model.PeerAgentM{
		Backend:   cr.Spec.Backend,
		Scope:     cr.Spec.Scope,
		PeerID:    cr.Spec.PeerID,
		Canonical: cr.Spec.Canonical,
		Agents:    jsonList(cr.Spec.Agents),
		Source:    cr.Spec.Source,
		UpdatedAt: updatedAt,
	}
}

func PeerBindingToCR(row *model.PeerAgentM, cr *chatv1alpha1.PeerBinding) {
	cr.Spec.Enabled = true
	cr.Spec.Backend = row.Backend
	cr.Spec.Scope = row.Scope
	cr.Spec.PeerID = row.PeerID
	cr.Spec.Canonical = row.Canonical
	cr.Spec.Agents = decodeList(row.Agents)
	cr.Spec.Source = row.Source
	if cr.Annotations == nil {
		cr.Annotations = make(map[string]string, 1)
	}
	cr.Annotations[annotationUpdatedAt] = row.UpdatedAt.UTC().Format(time.RFC3339)
}
