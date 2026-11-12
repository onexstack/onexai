// Copyright 2022 Lingfei Kong <colin404@foxmail.com>
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
//
// The original repo for this file is https://github.com/onexstack/onex.
//

package rest

import (
	"context"
	"errors"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/generic"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	api "k8s.io/kubernetes/pkg/apis/core"
	configmapstore "k8s.io/kubernetes/pkg/registry/core/configmap/storage"
	endpointsstore "k8s.io/kubernetes/pkg/registry/core/endpoint/storage"
	eventstore "k8s.io/kubernetes/pkg/registry/core/event/storage"
	namespacestore "k8s.io/kubernetes/pkg/registry/core/namespace/storage"
	secretstore "k8s.io/kubernetes/pkg/registry/core/secret/storage"
	servicestore "k8s.io/kubernetes/pkg/registry/core/service/storage"
	// configmapstore "github.com/onexstack/onexai/internal/registry/core/configmap/storage"
	// eventstore "github.com/onexstack/onexai/internal/registry/core/event/storage"
	// namespacestore "github.com/onexstack/onexai/internal/registry/core/namespace/storage".
)

// LegacyRESTStorageProvider provides information needed to build RESTStorage for kubernetes core, but
// does NOT implement the "normal" RESTStorageProvider (yet!)
type LegacyRESTStorageProvider struct {
	EventTTL time.Duration
}

// NewLegacyRESTStorage is a factory constructor to creates and returns the APIGroupInfo.
func (p LegacyRESTStorageProvider) NewLegacyRESTStorage(restOptionsGetter generic.RESTOptionsGetter) (genericapiserver.APIGroupInfo, error) {
	apiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(api.GroupName, legacyscheme.Scheme, legacyscheme.ParameterCodec, legacyscheme.Codecs)

	namespaceStorage, namespaceStatusStorage, namespaceFinalizeStorage, err := namespacestore.NewREST(restOptionsGetter)
	if err != nil {
		return genericapiserver.APIGroupInfo{}, err
	}

	eventStorage, err := eventstore.NewREST(restOptionsGetter, uint64(p.EventTTL.Seconds()))
	if err != nil {
		return genericapiserver.APIGroupInfo{}, err
	}

	configMapStorage, err := configmapstore.NewREST(restOptionsGetter)
	if err != nil {
		return genericapiserver.APIGroupInfo{}, err
	}

	secretStorage, err := secretstore.NewREST(restOptionsGetter)
	if err != nil {
		return genericapiserver.APIGroupInfo{}, err
	}

	endpointsStorage, err := endpointsstore.NewREST(restOptionsGetter)
	if err != nil {
		return genericapiserver.APIGroupInfo{}, err
	}

	// Services are served read-only. A Kubernetes WebhookClientConfig's
	// `service:` reference, APIService and the CRD conversion webhook all resolve
	// through a Service lister, and an informer cannot sync against a resource
	// the API server does not serve. The upstream Service storage is constructed
	// with nil allocators because this process does not run the controller that
	// would allocate ClusterIPs; read paths never touch them, and the read-only
	// wrapper keeps create/update off the surface so a caller cannot reach a
	// code path that needs an allocator.
	serviceStorage, _, _, err := servicestore.NewREST(
		restOptionsGetter, api.IPv4Protocol, nil, nil, nil, nil, nil,
	)
	if err != nil {
		return genericapiserver.APIGroupInfo{}, err
	}

	restStorageMap := map[string]rest.Storage{
		"namespaces":          namespaceStorage,
		"namespaces/status":   namespaceStatusStorage,
		"namespaces/finalize": namespaceFinalizeStorage,

		"events": eventStorage,

		"configmaps": configMapStorage,
		"secrets":    secretStorage,

		"endpoints": endpointsStorage,
		"services":  newReadOnlyStorage(serviceStorage),
	}

	apiGroupInfo.VersionedResourcesStorageMap[apiv1.SchemeGroupVersion.Version] = restStorageMap

	return apiGroupInfo, nil
}

// GroupName return the api group name.
func (p LegacyRESTStorageProvider) GroupName() string {
	return api.GroupName
}

// readOnlyStorage exposes a REST storage's read verbs only.
//
// It forwards the optional interfaces the API installer uses to decide a
// resource's scope and names. Without Scoper the installer would mount Services
// cluster-scoped, and the namespaced informer a ServiceResolver relies on would
// never sync.
type readOnlyStorage struct {
	rest.Storage
	getter rest.Getter
	lister rest.Lister
}

func newReadOnlyStorage(storage rest.Storage) readOnlyStorage {
	out := readOnlyStorage{Storage: storage}
	out.getter, _ = storage.(rest.Getter)
	out.lister, _ = storage.(rest.Lister)
	return out
}

// Get implements rest.Getter.
func (s readOnlyStorage) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	if s.getter == nil {
		return nil, errors.New("the storage does not support get")
	}
	return s.getter.Get(ctx, name, options)
}

// NewList implements rest.Lister.
func (s readOnlyStorage) NewList() runtime.Object {
	if s.lister == nil {
		return nil
	}
	return s.lister.NewList()
}

// List implements rest.Lister.
func (s readOnlyStorage) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	if s.lister == nil {
		return nil, errors.New("the storage does not support list")
	}
	return s.lister.List(ctx, options)
}

// Watch implements rest.Watcher. An informer cannot sync without it: list-only
// storage makes every reflector retry "watch is not supported" forever.
func (s readOnlyStorage) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	if watcher, ok := s.Storage.(rest.Watcher); ok {
		return watcher.Watch(ctx, options)
	}
	return nil, errors.New("the storage does not support watch")
}

// ConvertToTable implements rest.Lister's embedded TableConvertor.
func (s readOnlyStorage) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	if converter, ok := s.Storage.(rest.TableConvertor); ok {
		return converter.ConvertToTable(ctx, object, tableOptions)
	}
	return rest.NewDefaultTableConvertor(api.Resource("services")).ConvertToTable(ctx, object, tableOptions)
}

// NamespaceScoped implements rest.Scoper.
func (s readOnlyStorage) NamespaceScoped() bool {
	if scoper, ok := s.Storage.(rest.Scoper); ok {
		return scoper.NamespaceScoped()
	}
	return false
}

// GetSingularName implements rest.SingularNameProvider.
func (s readOnlyStorage) GetSingularName() string {
	if provider, ok := s.Storage.(rest.SingularNameProvider); ok {
		return provider.GetSingularName()
	}
	return ""
}

// ShortNames implements rest.ShortNamesProvider.
func (s readOnlyStorage) ShortNames() []string {
	if provider, ok := s.Storage.(rest.ShortNamesProvider); ok {
		return provider.ShortNames()
	}
	return nil
}

// Categories implements rest.CategoriesProvider.
func (s readOnlyStorage) Categories() []string {
	if provider, ok := s.Storage.(rest.CategoriesProvider); ok {
		return provider.Categories()
	}
	return nil
}
