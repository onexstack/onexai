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

// customCommandStore is the custom commands accessor over the control plane.
type customCommandStore struct {
	*Store[model.CustomCommandM]
}

// Ensure customCommandStore implements the CustomCommandStore interface.
var _ store.CustomCommandStore = (*customCommandStore)(nil)

// NewCustomCommandStore builds the custom commands accessor.
func NewCustomCommandStore(client *declclient.Client, namespace string) *customCommandStore {
	lister := client.CustomCommands()
	list := func() ([]*agentv1alpha1.CustomCommand, error) { return lister.List(labels.Everything()) }

	return &customCommandStore{Store: declcrud.NewStore(declcrud.Spec[model.CustomCommandM, *agentv1alpha1.CustomCommand]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.CustomCommand] {
			return client.Clientset.AgentV1alpha1().CustomCommands(namespace)
		},
		GetByKey: getByKey(lister.CustomCommands(namespace).Get, customCommandFromCR),
		List:     list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.CustomCommand) (model.CustomCommandM, error) {
			return customCommandFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.CustomCommandM, cr *agentv1alpha1.CustomCommand) error {
			return CustomCommandToCR(row, cr)
		},
		New: func(name, namespace string) *agentv1alpha1.CustomCommand {
			return &agentv1alpha1.CustomCommand{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:    "name",
		KeyOf:  func(row model.CustomCommandM) string { return row.Name },
		NameOf: func(row model.CustomCommandM) string { return row.Name },
		Column: customCommandColumn,
		Namespaces: rowNamespaces(namespace, list,
			func(row model.CustomCommandM) string { return rowNamespace(row.Namespace, namespace) }),
	})}
}

func customCommandColumn(row model.CustomCommandM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	case "workspace", "namespace":
		return row.Namespace, true
	case "type":
		return row.Type, true
	default:
		return "", false
	}
}
