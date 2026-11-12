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

// webhookStore is the webhooks accessor over the control plane.
type webhookStore struct {
	*Store[model.WebhookM]
}

// Ensure webhookStore implements the WebhookStore interface.
var _ store.WebhookStore = (*webhookStore)(nil)

// NewWebhookStore builds the webhook accessor.
func NewWebhookStore(client *declclient.Client, namespace string) *webhookStore {
	lister := client.Webhooks()
	list := func() ([]*agentv1alpha1.Webhook, error) { return lister.List(labels.Everything()) }

	return &webhookStore{Store: declcrud.NewStore(declcrud.Spec[model.WebhookM, *agentv1alpha1.Webhook]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.Webhook] {
			return client.Clientset.AgentV1alpha1().Webhooks(namespace)
		},
		GetByKey: getByKey(lister.Webhooks(namespace).Get, webhookFromCR),
		List:     list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.Webhook) (model.WebhookM, error) {
			return webhookFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.WebhookM, cr *agentv1alpha1.Webhook) error {
			WebhookToCR(row, cr)
			return nil
		},
		New: func(name, namespace string) *agentv1alpha1.Webhook {
			return &agentv1alpha1.Webhook{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:        "name",
		KeyOf:      func(row model.WebhookM) string { return row.Name },
		NameOf:     func(row model.WebhookM) string { return row.Name },
		Column:     webhookColumn,
		Namespaces: fixedNamespaces[model.WebhookM](namespace, list),
	})}
}

// webhookColumn reads a filterable column off a webhook row.
func webhookColumn(row model.WebhookM, name string) (string, bool) {
	if name == "name" {
		return row.Name, true
	}
	return "", false
}
