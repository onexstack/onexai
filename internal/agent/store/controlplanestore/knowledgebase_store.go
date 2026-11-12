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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/declcrud"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// knowledgeBaseStore is the knowledge bases accessor over the control plane.
type knowledgeBaseStore struct {
	*Store[model.KnowledgeBaseM]
}

// Ensure knowledgeBaseStore implements the KnowledgeBaseStore interface.
var _ store.KnowledgeBaseStore = (*knowledgeBaseStore)(nil)

// NewKnowledgeBaseStore builds the knowledge base accessor.
func NewKnowledgeBaseStore(client *declclient.Client, namespace string) *knowledgeBaseStore {
	lister := client.KnowledgeBases()
	list := func() ([]*agentv1alpha1.KnowledgeBase, error) { return lister.List(labels.Everything()) }

	return &knowledgeBaseStore{Store: declcrud.NewStore(declcrud.Spec[model.KnowledgeBaseM, *agentv1alpha1.KnowledgeBase]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.KnowledgeBase] {
			return client.Clientset.AgentV1alpha1().KnowledgeBases(namespace)
		},
		GetByKey: getByKey(lister.KnowledgeBases(namespace).Get, knowledgeBaseFromCR),
		GetByNamespacedKey: getByNamespacedKey(func(ns, name string) (*agentv1alpha1.KnowledgeBase, error) {
			return lister.KnowledgeBases(ns).Get(name)
		}, knowledgeBaseFromCR),
		List: list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.KnowledgeBase) (model.KnowledgeBaseM, error) {
			return knowledgeBaseFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.KnowledgeBaseM, cr *agentv1alpha1.KnowledgeBase) error {
			KnowledgeBaseToCR(row, cr)
			return nil
		},
		New: func(name, namespace string) *agentv1alpha1.KnowledgeBase {
			return &agentv1alpha1.KnowledgeBase{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:    "name",
		KeyOf:  func(row model.KnowledgeBaseM) string { return row.Name },
		NameOf: func(row model.KnowledgeBaseM) string { return row.Name },
		Column: knowledgeBaseColumn,
		Namespaces: rowNamespaces(namespace, list,
			func(row model.KnowledgeBaseM) string { return rowNamespace(row.Namespace, namespace) }),
	})}
}

// knowledgeBaseColumn reads a filterable column off a knowledge-base row.
func knowledgeBaseColumn(row model.KnowledgeBaseM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	case "workspace", "namespace":
		return row.Namespace, true
	case "platform":
		return row.Platform, true
	default:
		return "", false
	}
}
