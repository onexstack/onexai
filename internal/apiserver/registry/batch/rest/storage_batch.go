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

// Package rest registers the batch API group with the declarative API server.
package rest

import (
	"k8s.io/apiserver/pkg/registry/generic"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	serverstorage "k8s.io/apiserver/pkg/server/storage"
	"k8s.io/kubernetes/pkg/api/legacyscheme"

	cronjobstore "github.com/onexstack/onexai/internal/apiserver/registry/batch/cronjob/storage"
	jobstore "github.com/onexstack/onexai/internal/apiserver/registry/batch/job/storage"
	"github.com/onexstack/onexai/pkg/apis/batch"
	"github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
	"github.com/onexstack/onexai/pkg/apiserver/storage"
)

// RESTStorageProvider is a struct for batch REST storage.
type RESTStorageProvider struct{}

var _ storage.RESTStorageProvider = &RESTStorageProvider{}

// NewRESTStorage returns APIGroupInfo.
func (p RESTStorageProvider) NewRESTStorage(
	apiResourceConfigSource serverstorage.APIResourceConfigSource,
	restOptionsGetter generic.RESTOptionsGetter,
) (genericapiserver.APIGroupInfo, error) {
	apiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(
		batch.GroupName, legacyscheme.Scheme, legacyscheme.ParameterCodec, legacyscheme.Codecs,
	)

	storageMap, err := p.v1beta1Storage(apiResourceConfigSource, restOptionsGetter)
	if err != nil {
		return genericapiserver.APIGroupInfo{}, err
	}
	apiGroupInfo.VersionedResourcesStorageMap[v1beta1.SchemeGroupVersion.Version] = storageMap

	return apiGroupInfo, nil
}

func (p RESTStorageProvider) v1beta1Storage(
	apiResourceConfigSource serverstorage.APIResourceConfigSource,
	restOptionsGetter generic.RESTOptionsGetter,
) (map[string]rest.Storage, error) {
	storages := map[string]rest.Storage{}

	if resource := "cronjobs"; apiResourceConfigSource.ResourceEnabled(v1beta1.SchemeGroupVersion.WithResource(resource)) {
		cronJobStorage, err := cronjobstore.NewStorage(restOptionsGetter)
		if err != nil {
			return storages, err
		}
		storages[resource] = cronJobStorage.CronJob
		storages[resource+"/status"] = cronJobStorage.Status
	}

	if resource := "jobs"; apiResourceConfigSource.ResourceEnabled(v1beta1.SchemeGroupVersion.WithResource(resource)) {
		jobStorage, err := jobstore.NewStorage(restOptionsGetter)
		if err != nil {
			return storages, err
		}
		storages[resource] = jobStorage.Job
		storages[resource+"/status"] = jobStorage.Status
	}

	return storages, nil
}

// GroupName returns the API group name.
func (p RESTStorageProvider) GroupName() string { return batch.GroupName }
