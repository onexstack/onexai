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

package storage

import (
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/registry/generic"
	genericapiserver "k8s.io/apiserver/pkg/server"
	serverstorage "k8s.io/apiserver/pkg/server/storage"
	"k8s.io/client-go/tools/record"
)

// WriteAuthorizerSetter is implemented by a storage whose write path has to
// consult the server's authorizer and report what it decided.
//
// The server calls it after every storage is built, for every API group, so a
// resource that needs the check does not have to be special-cased in the
// scaffolded provider: the provider stays generic, and the behaviour lives in
// the resource's own package. A storage that does not implement this interface
// simply performs no write-time authorization of its own.
type WriteAuthorizerSetter interface {
	SetWriteAuthorizer(authorizer.Authorizer, record.EventRecorder)
}

// RESTStorageProvider is a factory type for REST storage.
type RESTStorageProvider interface {
	GroupName() string
	NewRESTStorage(apiResourceConfigSource serverstorage.APIResourceConfigSource, restOptionsGetter generic.RESTOptionsGetter) (genericapiserver.APIGroupInfo, error)
}
