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
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/pkg/declclient"
)

func TestNamespaceStoreCreateWritesTheNamespace(t *testing.T) {
	kube := fake.NewSimpleClientset()
	store := NewNamespaceStore(&declclient.Client{KubeClient: kube})

	row := &model.NamespaceM{
		Name:        "team-a",
		DisplayName: "Team A",
		Description: "A business line",
		Owner:       "alice",
		Labels:      model.StringMap{"team": "a"},
	}
	if err := store.Create(context.Background(), row); err != nil {
		t.Fatalf("Create: %v", err)
	}

	namespace, err := kube.CoreV1().Namespaces().Get(context.Background(), "team-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get created namespace: %v", err)
	}
	if namespace.Labels["team"] != "a" ||
		namespace.Annotations[model.AnnotationDisplayName] != "Team A" ||
		namespace.Annotations[model.AnnotationOwner] != "alice" {
		t.Fatalf("namespace metadata not written: %+v", namespace.ObjectMeta)
	}
	if namespace.Annotations[model.AnnotationUpdatedAt] == "" {
		t.Fatal("updated-at annotation was not written")
	}
}

func TestNamespaceStoreUpdateAnnotatesAndClearsEmptyValues(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "team-a",
			Annotations: map[string]string{
				model.AnnotationDisplayName: "Old name",
				model.AnnotationOwner:       "alice",
			},
		},
	})
	store := NewNamespaceStore(&declclient.Client{KubeClient: kube})

	if err := store.Update(context.Background(), &model.NamespaceM{
		Name:        "team-a",
		DisplayName: "Team A",
		Owner:       "",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	namespace, err := kube.CoreV1().Namespaces().Get(context.Background(), "team-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get updated namespace: %v", err)
	}
	if namespace.Annotations[model.AnnotationDisplayName] != "Team A" {
		t.Fatalf("display name not updated: %v", namespace.Annotations)
	}
	if _, ok := namespace.Annotations[model.AnnotationOwner]; ok {
		t.Fatalf("clearing the owner must delete its annotation, got %v", namespace.Annotations)
	}
}

func TestNamespaceStoreDeleteIsIdempotent(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "team-a"},
	})
	store := NewNamespaceStore(&declclient.Client{KubeClient: kube})

	if err := store.Delete(context.Background(), where.F("name", "team-a")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(context.Background(), where.F("name", "team-a")); err != nil {
		t.Fatalf("second Delete should be a no-op: %v", err)
	}
}

func TestNamespaceStoreGetHidesTerminatingNamespaces(t *testing.T) {
	now := metav1.NewTime(time.Now())
	kube := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "team-a", DeletionTimestamp: &now},
	})
	store := NewNamespaceStore(&declclient.Client{KubeClient: kube})

	row, err := store.Get(context.Background(), where.F("name", "team-a"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row != nil {
		t.Fatalf("a terminating namespace must not be a business line, got %+v", row)
	}
}

func TestNamespaceStoreListFiltersAndPaginates(t *testing.T) {
	kube := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
	)
	store := NewNamespaceStore(&declclient.Client{KubeClient: kube})

	total, list, err := store.List(context.Background(), where.NewWhere().O(0).L(10))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("List = %d/%d, want 2/2 (system namespaces filtered)", total, len(list))
	}
	if list[0].Name != "team-a" || list[1].Name != "team-b" {
		t.Fatalf("List order = %v, want [team-a team-b]", []string{list[0].Name, list[1].Name})
	}

	total, page, err := store.List(context.Background(), where.NewWhere().O(1).L(1))
	if err != nil {
		t.Fatalf("List page: %v", err)
	}
	if total != 2 || len(page) != 1 || page[0].Name != "team-b" {
		t.Fatalf("List page = %d/%v, want 2/[team-b]", total, page)
	}
}

func TestNamespaceStoreGetReadsBusinessAnnotations(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "team-a",
			CreationTimestamp: metav1.NewTime(time.UnixMilli(1000)),
			Labels:            map[string]string{"team": "a"},
			Annotations: map[string]string{
				model.AnnotationDisplayName: "Team A",
				model.AnnotationDescription: "desc",
				model.AnnotationOwner:       "alice",
				model.AnnotationUpdatedAt:   "2000",
			},
		},
	})
	store := NewNamespaceStore(&declclient.Client{KubeClient: kube})

	row, err := store.Get(context.Background(), where.F("name", "team-a"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row == nil || row.DisplayName != "Team A" || row.Description != "desc" || row.Owner != "alice" {
		t.Fatalf("decoded row = %+v", row)
	}
	if row.Labels["team"] != "a" {
		t.Fatalf("labels = %v", row.Labels)
	}
	if row.CreatedAt != 1000 || row.UpdatedAt != 2000 {
		t.Fatalf("timestamps = %d/%d", row.CreatedAt, row.UpdatedAt)
	}
}
