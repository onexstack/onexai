// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package controlplane

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/registry/generic"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	serverstorage "k8s.io/apiserver/pkg/server/storage"
	"k8s.io/client-go/tools/record"
)

// awareStorage asks for the authorizer; plainStorage does not.
type awareStorage struct {
	authorizer authorizer.Authorizer
	recorder   record.EventRecorder
}

func (s *awareStorage) New() runtime.Object { return nil }
func (s *awareStorage) Destroy()            {}
func (s *awareStorage) SetWriteAuthorizer(a authorizer.Authorizer, r record.EventRecorder) {
	s.authorizer = a
	s.recorder = r
}

type plainStorage struct{}

func (s *plainStorage) New() runtime.Object { return nil }
func (s *plainStorage) Destroy()            {}

type fakeProvider struct {
	groupName string
	storages  map[string]rest.Storage
}

func (p *fakeProvider) GroupName() string { return p.groupName }
func (p *fakeProvider) NewRESTStorage(
	serverstorage.APIResourceConfigSource, generic.RESTOptionsGetter,
) (genericapiserver.APIGroupInfo, error) {
	return genericapiserver.APIGroupInfo{
		VersionedResourcesStorageMap: map[string]map[string]rest.Storage{"v1alpha1": p.storages},
	}, nil
}

func TestAuthorizingProviderDecoratesOnlyAwareStorages(t *testing.T) {
	aware := &awareStorage{}
	plain := &plainStorage{}
	provider := &authorizingProvider{
		inner: &fakeProvider{
			groupName: "agent.onexai.io",
			storages: map[string]rest.Storage{
				"agentbindings":        aware,
				"agentbindings/status": plain,
			},
		},
		authorizer: authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
			return authorizer.DecisionAllow, "", nil
		}),
		recorder: record.NewFakeRecorder(1),
	}

	if _, err := provider.NewRESTStorage(nil, nil); err != nil {
		t.Fatalf("NewRESTStorage: %v", err)
	}

	if aware.authorizer == nil {
		t.Error("the aware storage did not receive the authorizer")
	}
	if aware.recorder == nil {
		t.Error("the aware storage did not receive the recorder")
	}
}
