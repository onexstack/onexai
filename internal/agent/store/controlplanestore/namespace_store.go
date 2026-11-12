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
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/pkg/declclient"
	"github.com/onexstack/onexai/pkg/readmodel"
)

// namespaceStore serves a business line from the Kubernetes Namespace object
// itself.
//
// # Why there is no second store
//
// A Namespace already carries everything a business line needs: a name, labels,
// annotations and a lifecycle. Keeping a row in PostgreSQL as well meant every
// write had to succeed twice and a reverse syncer had to repair the two when
// they disagreed. The object is now the only source of truth; the console's
// fields are annotations on it.
//
// # Reads and writes
//
// Get goes to the API server, not the informer, so a create or update is
// visible to its own read. List reads the informer cache because it is a
// cluster-wide collection and namespaces are few; a cache that has not synced
// falls back to a live list rather than answering an empty page.
type namespaceStore struct {
	client kubernetes.Interface
	lister corelisters.NamespaceLister
}

var _ agentstore.NamespaceStore = (*namespaceStore)(nil)

// NewNamespaceStore builds the business-line accessor over the control plane's
// Kubernetes client. A nil client is a deployment without a control plane; the
// store fails closed on every write and answers no business lines on reads.
func NewNamespaceStore(client *declclient.Client) *namespaceStore {
	var kube kubernetes.Interface
	var lister corelisters.NamespaceLister
	if client != nil {
		kube = client.KubeClient
		if client.KubeFactory != nil {
			// declclient registers this informer before starting the factory; an
			// informer requested after Start never runs. This accessor only
			// borrows its indexer.
			lister = corelisters.NewNamespaceLister(
				client.KubeFactory.Core().V1().Namespaces().Informer().GetIndexer(),
			)
		}
	}
	return &namespaceStore{client: kube, lister: lister}
}

// Create declares the namespace in the control plane.
func (s *namespaceStore) Create(ctx context.Context, obj *model.NamespaceM) error {
	if s.client == nil {
		return errNamespaceNeedsControlPlane
	}
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        obj.Name,
			Labels:      labelsOf(obj),
			Annotations: annotationsOf(obj),
		},
	}
	created, err := s.client.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	applyNamespace(created, obj)
	return nil
}

// Update refreshes the business metadata on an existing Namespace.
//
// Only the annotation keys this package owns are touched, and an empty value
// deletes its key. The old implementation only wrote non-empty values, so
// clearing a display name or owner left the previous value behind.
func (s *namespaceStore) Update(ctx context.Context, obj *model.NamespaceM) error {
	if s.client == nil {
		return errNamespaceNeedsControlPlane
	}
	namespaces := s.client.CoreV1().Namespaces()
	current, err := namespaces.Get(ctx, obj.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	updated := current.DeepCopy()
	updated.Labels = labelsOf(obj)
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	mergeOwnedAnnotations(updated.Annotations, obj)
	_, err = namespaces.Update(ctx, updated, metav1.UpdateOptions{})
	return err
}

// Delete removes the namespace from the control plane. A namespace that is
// already gone is success: a retried delete must not surface as an error.
func (s *namespaceStore) Delete(ctx context.Context, opts *where.Options) error {
	if s.client == nil {
		return errNamespaceNeedsControlPlane
	}
	name, err := nameFromOptions(opts)
	if err != nil {
		return err
	}
	if err := s.client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{}); err != nil &&
		!apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// Get reads the namespace from the API server. A missing namespace, or one that
// is terminating, answers (nil, nil): the caller sees the same "not found" a
// row store used to produce, and a namespace being deleted cannot be used to
// place new resources.
func (s *namespaceStore) Get(ctx context.Context, opts *where.Options) (*model.NamespaceM, error) {
	if s.client == nil {
		return nil, nil
	}
	name, err := nameFromOptions(opts)
	if err != nil {
		return nil, err
	}
	namespace, err := s.client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if namespace.DeletionTimestamp != nil {
		return nil, nil
	}
	return namespaceModel(namespace), nil
}

// List returns the business lines, sorted by name. System namespaces and
// namespaces being deleted are not business lines and are filtered out.
func (s *namespaceStore) List(ctx context.Context, opts *where.Options) (int64, []*model.NamespaceM, error) {
	items, err := s.list(ctx)
	if err != nil {
		return 0, nil, err
	}

	offset, limit := 0, -1
	if opts != nil {
		offset, limit = opts.Offset, opts.Limit
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := len(items)
	if limit >= 0 && offset+limit < end {
		end = offset + limit
	}

	out := make([]*model.NamespaceM, 0, end-offset)
	for _, namespace := range items[offset:end] {
		out = append(out, namespaceModel(namespace))
	}
	return int64(len(items)), out, nil
}

// list reads every namespace, preferring the informer and falling back to a
// live list when the cache is empty.
//
// An empty cache is never a real answer: a control plane always holds its own
// system namespaces. It means the informer did not run (a lister whose informer
// was registered after the factory started answers an empty list with no
// error), so reading the API server is the only way to avoid reporting a
// deployment with no business lines. Namespaces are few, so the fallback is
// cheap.
func (s *namespaceStore) list(ctx context.Context) ([]*corev1.Namespace, error) {
	var items []*corev1.Namespace
	if s.lister != nil {
		var err error
		items, err = s.lister.List(labels.Everything())
		if err != nil {
			return nil, err
		}
	}
	if len(items) == 0 && s.client != nil {
		list, err := s.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		items = make([]*corev1.Namespace, 0, len(list.Items))
		for i := range list.Items {
			items = append(items, &list.Items[i])
		}
	}

	out := make([]*corev1.Namespace, 0, len(items))
	for _, namespace := range items {
		if readmodel.SystemNamespace(namespace.Name) || namespace.DeletionTimestamp != nil {
			continue
		}
		out = append(out, namespace)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// namespaceModel maps a Namespace onto the view the API layer serves.
func namespaceModel(namespace *corev1.Namespace) *model.NamespaceM {
	row := &model.NamespaceM{
		Name:        namespace.Name,
		DisplayName: namespace.Annotations[model.AnnotationDisplayName],
		Description: namespace.Annotations[model.AnnotationDescription],
		Owner:       namespace.Annotations[model.AnnotationOwner],
		Labels:      model.StringMap(namespace.Labels),
		CreatedAt:   namespace.CreationTimestamp.UnixMilli(),
		UpdatedAt:   parseMillis(namespace.Annotations[model.AnnotationUpdatedAt]),
	}
	if row.DisplayName == "" {
		row.DisplayName = namespace.Name
	}
	if row.Labels == nil {
		row.Labels = model.StringMap{}
	}
	if row.UpdatedAt == 0 {
		row.UpdatedAt = row.CreatedAt
	}
	return row
}

// applyNamespace fills the view from the object the API server returned, so a
// create reports the server-generated timestamps rather than the caller's
// guesses.
func applyNamespace(namespace *corev1.Namespace, obj *model.NamespaceM) {
	view := namespaceModel(namespace)
	obj.ID = view.ID
	obj.CreatedAt = view.CreatedAt
	obj.UpdatedAt = view.UpdatedAt
}

// annotationsOf builds the annotations for a new Namespace.
func annotationsOf(obj *model.NamespaceM) map[string]string {
	annotations := map[string]string{}
	mergeOwnedAnnotations(annotations, obj)
	return annotations
}

// mergeOwnedAnnotations writes the keys this package owns onto an existing map.
// An empty business value deletes its key rather than leaving a stale value.
func mergeOwnedAnnotations(annotations map[string]string, obj *model.NamespaceM) {
	setOrDelete(annotations, model.AnnotationDisplayName, obj.DisplayName)
	setOrDelete(annotations, model.AnnotationDescription, obj.Description)
	setOrDelete(annotations, model.AnnotationOwner, obj.Owner)

	updatedAt := obj.UpdatedAt
	if updatedAt == 0 {
		updatedAt = time.Now().UnixMilli()
	}
	annotations[model.AnnotationUpdatedAt] = strconv.FormatInt(updatedAt, 10)
}

func setOrDelete(annotations map[string]string, key, value string) {
	if value == "" {
		delete(annotations, key)
		return
	}
	annotations[key] = value
}

// labelsOf returns the labels to write. A nil map is left nil so an update does
// not materialise an empty `{}` on an object that had none.
func labelsOf(obj *model.NamespaceM) map[string]string {
	if len(obj.Labels) == 0 {
		return nil
	}
	return map[string]string(obj.Labels)
}

func parseMillis(value string) int64 {
	if value == "" {
		return 0
	}
	millis, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return millis
}

// nameFromOptions extracts the address from the standard where options.
func nameFromOptions(opts *where.Options) (string, error) {
	if opts != nil {
		if name, ok := opts.Filters["name"].(string); ok && name != "" {
			return name, nil
		}
	}
	return "", errors.New("namespace name is required")
}

var errNamespaceNeedsControlPlane = errors.New(
	"business lines need the declarative control plane; set controlplane.kubeconfig",
)
