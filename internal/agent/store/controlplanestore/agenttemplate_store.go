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
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/declcrud"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// agentTemplateStore is the agent templates accessor over the control plane.
type agentTemplateStore struct {
	*Store[model.AgentTemplateM]
}

// Ensure agentTemplateStore implements the AgentTemplateStore interface.
var _ store.AgentTemplateStore = (*agentTemplateStore)(nil)

// agentTemplateColumn maps the fields a filter may name to the row's value.
func agentTemplateColumn(row model.AgentTemplateM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	default:
		return "", false
	}
}

// NewAgentTemplateStore builds the agent template accessor.
func NewAgentTemplateStore(client *declclient.Client, namespace string) *agentTemplateStore {
	lister := client.AgentTemplates()
	list := func() ([]*agentv1alpha1.AgentTemplate, error) { return lister.List(labels.Everything()) }

	return &agentTemplateStore{Store: declcrud.NewStore(declcrud.Spec[model.AgentTemplateM, *agentv1alpha1.AgentTemplate]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.AgentTemplate] {
			return client.Clientset.AgentV1alpha1().AgentTemplates(namespace)
		},
		GetByKey: getByKey(lister.AgentTemplates(namespace).Get, agentTemplateFromCR),
		List:     list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.AgentTemplate) (model.AgentTemplateM, error) {
			return agentTemplateFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.AgentTemplateM, cr *agentv1alpha1.AgentTemplate) error {
			return AgentTemplateToCR(row, cr)
		},
		New: func(name, namespace string) *agentv1alpha1.AgentTemplate {
			return &agentv1alpha1.AgentTemplate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:    "name",
		KeyOf:  func(row model.AgentTemplateM) string { return row.Name },
		NameOf: func(row model.AgentTemplateM) string { return row.Name },
		Column: agentTemplateColumn,
		Namespaces: rowNamespaces(namespace, list,
			func(row model.AgentTemplateM) string { return rowNamespace(row.Namespace, namespace) }),
	})}
}

// agentTemplateFromCR maps one object onto the row the runtime reads.
func agentTemplateFromCR(cr *agentv1alpha1.AgentTemplate) model.AgentTemplateM {
	spec, err := json.Marshal(cr.Spec)
	if err != nil {
		spec = nil
	}
	return model.AgentTemplateM{
		Name:        cr.Name,
		DisplayName: cr.Spec.DisplayName,
		Description: cr.Spec.Description,
		Status:      statusOf(cr.Spec.Enabled),
		Spec:        string(spec),
		Namespace:   cr.Namespace,
		Labels:      labelsFrom(cr),
		CreatedAt:   unixMilli(cr.CreationTimestamp),
		UpdatedAt:   unixMilli(cr.CreationTimestamp),
	}
}

// AgentTemplateToCR maps a row back onto the object the admin API writes.
func AgentTemplateToCR(row *model.AgentTemplateM, cr *agentv1alpha1.AgentTemplate) error {
	if row.Spec != "" {
		var spec agentv1alpha1.AgentTemplateSpec
		if err := json.Unmarshal([]byte(row.Spec), &spec); err != nil {
			return fmt.Errorf("decoding spec of agent template %q: %w", row.Name, err)
		}
		cr.Spec = spec
	}
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	setLabels(cr, row.Labels)
	return nil
}
