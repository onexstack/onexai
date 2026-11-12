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

package declclient

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	kubeinformers "k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	corelisters "k8s.io/client-go/listers/core/v1"

	projectinformers "github.com/onexstack/onexai/pkg/generated/informers/externalversions"
)

// TestNamespacesInformerIsRegisteredBeforeStart guards the failure mode the
// namespace store hit: it builds its lister after declclient.Start has run, so
// a Namespaces informer requested at that point never syncs and List answers an
// empty page with no error.
func TestNamespacesInformerIsRegisteredBeforeStart(t *testing.T) {
	kube := k8sfake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "helper-public"},
	})
	client := &Client{
		// Only KubeFactory is started; the project factory needs no API server
		// for this test, so a nil clientset is enough to build its informers.
		Factory:     projectinformers.NewSharedInformerFactory(nil, 0),
		KubeFactory: kubeinformers.NewSharedInformerFactory(kube, 0),
	}
	client.registerInformers()

	stop := make(chan struct{})
	defer close(stop)
	client.KubeFactory.Start(stop)
	for typ, synced := range client.KubeFactory.WaitForCacheSync(stop) {
		if !synced {
			t.Fatalf("informer for %v did not sync", typ)
		}
	}

	namespaces, err := corelisters.NewNamespaceLister(
		client.KubeFactory.Core().V1().Namespaces().Informer().GetIndexer(),
	).List(labels.Everything())
	if err != nil {
		t.Fatalf("list namespaces: %v", err)
	}
	if len(namespaces) != 1 || namespaces[0].Name != "helper-public" {
		t.Fatalf("namespace lister = %v, want [helper-public]; "+
			"the Namespaces informer must be registered before the factory starts", namespaces)
	}
}
