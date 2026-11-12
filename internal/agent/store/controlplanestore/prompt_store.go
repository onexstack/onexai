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

// promptStore is the prompts accessor over the control plane.
type promptStore struct {
	*Store[model.PromptM]
}

// Ensure promptStore implements the PromptStore interface.
var _ store.PromptStore = (*promptStore)(nil)

// NewPromptStore builds the prompts accessor.
//
// A prompt row carries its namespace, so a create or update lands in the row's
// own namespace and falls back to the store's configured one (else the default)
// only when the row names none.
func NewPromptStore(client *declclient.Client, namespace string) *promptStore {
	lister := client.Prompts()
	list := func() ([]*agentv1alpha1.Prompt, error) { return lister.List(labels.Everything()) }

	return &promptStore{Store: declcrud.NewStore(declcrud.Spec[model.PromptM, *agentv1alpha1.Prompt]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.Prompt] {
			return client.Clientset.AgentV1alpha1().Prompts(namespace)
		},
		GetByKey: getByKey(lister.Prompts(namespace).Get, promptFromCR),
		GetByNamespacedKey: getByNamespacedKey(func(ns, name string) (*agentv1alpha1.Prompt, error) {
			return lister.Prompts(ns).Get(name)
		}, promptFromCR),
		List: list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.Prompt) (model.PromptM, error) {
			return promptFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.PromptM, cr *agentv1alpha1.Prompt) error {
			PromptToCR(row, cr)
			return nil
		},
		New: func(name, namespace string) *agentv1alpha1.Prompt {
			return &agentv1alpha1.Prompt{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:    "name",
		KeyOf:  func(row model.PromptM) string { return row.Name },
		NameOf: func(row model.PromptM) string { return row.Name },
		Column: promptColumn,
		Namespaces: rowNamespaces(namespace, list,
			func(row model.PromptM) string { return rowNamespace(row.Namespace, namespace) }),
	})}
}

// promptColumn reads a filterable column off a prompt row.
func promptColumn(row model.PromptM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	case "workspace", "namespace":
		return row.Namespace, true
	default:
		return "", false
	}
}
