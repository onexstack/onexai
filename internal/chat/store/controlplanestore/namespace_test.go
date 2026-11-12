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
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// A deployment that watches every namespace configures none, but a namespaced
// object has no cluster-wide URL. Resolution has to come from the cache, or the
// write is answered with 405 and the console reports a binding whose bot-side
// projection was never written.
func TestResolvePeerBindingNamespacePrefersTheObject(t *testing.T) {
	list := func() ([]*chatv1alpha1.PeerBinding, error) {
		return []*chatv1alpha1.PeerBinding{
			{ObjectMeta: metav1.ObjectMeta{Name: "pb-a", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "pb-b", Namespace: "onex"}},
		}, nil
	}

	got, err := resolvePeerBindingNamespace(list, "pb-b", "default")
	if err != nil || got != "onex" {
		t.Errorf("namespace = %q, err = %v, want onex", got, err)
	}
	got, err = resolvePeerBindingNamespace(list, "pb-a", "fallback")
	if err != nil || got != "default" {
		t.Errorf("namespace = %q, err = %v, want the object's default", got, err)
	}
}

func TestResolvePeerBindingNamespaceFallsBackWhenTheCacheMisses(t *testing.T) {
	list := func() ([]*chatv1alpha1.PeerBinding, error) {
		return nil, errors.New("informer cache is not synced")
	}

	got, err := resolvePeerBindingNamespace(list, "pb-a", "default")
	if err != nil || got != "default" {
		t.Errorf("namespace = %q, err = %v, want default", got, err)
	}
}

func TestPeerBindingWriteNamespaceDefaultsToDefault(t *testing.T) {
	if got := peerBindingWriteNamespace(""); got != metav1.NamespaceDefault {
		t.Errorf("namespace = %q, want %q", got, metav1.NamespaceDefault)
	}
	if got := peerBindingWriteNamespace("onex"); got != "onex" {
		t.Errorf("namespace = %q, want onex", got)
	}
}
