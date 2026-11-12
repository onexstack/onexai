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
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNamespacesReturnsTheControlPlaneNamespacesSorted(t *testing.T) {
	client := &Client{KubeClient: fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "onex"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "helper-public"}},
	)}

	got, err := client.Namespaces(context.Background())
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	want := []string{"helper-public", "onex"}
	if len(got) != len(want) {
		t.Fatalf("Namespaces() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Namespaces() = %v, want %v", got, want)
		}
	}
}

func TestNamespacesWithoutKubeClientIsEmptyNotAnError(t *testing.T) {
	got, err := (&Client{}).Namespaces(context.Background())
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Namespaces() = %v, want an empty list", got)
	}
}
