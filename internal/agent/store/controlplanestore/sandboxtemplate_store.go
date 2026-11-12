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

// sandboxTemplateStore is the sandbox templates accessor over the control
// plane.
type sandboxTemplateStore struct {
	*Store[model.SandboxTemplateM]
}

// Ensure sandboxTemplateStore implements the SandboxTemplateStore interface.
var _ store.SandboxTemplateStore = (*sandboxTemplateStore)(nil)

// sandboxTemplateColumn maps the fields a filter may name to the row's value.
func sandboxTemplateColumn(row model.SandboxTemplateM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	default:
		return "", false
	}
}

// NewSandboxTemplateStore builds the sandbox template accessor.
func NewSandboxTemplateStore(client *declclient.Client, namespace string) *sandboxTemplateStore {
	lister := client.SandboxTemplates()
	list := func() ([]*agentv1alpha1.SandboxTemplate, error) { return lister.List(labels.Everything()) }

	return &sandboxTemplateStore{Store: declcrud.NewStore(declcrud.Spec[model.SandboxTemplateM, *agentv1alpha1.SandboxTemplate]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.SandboxTemplate] {
			return client.Clientset.AgentV1alpha1().SandboxTemplates(namespace)
		},
		GetByKey: getByKey(lister.SandboxTemplates(namespace).Get, sandboxTemplateFromCR),
		List:     list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.SandboxTemplate) (model.SandboxTemplateM, error) {
			return sandboxTemplateFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.SandboxTemplateM, cr *agentv1alpha1.SandboxTemplate) error {
			SandboxTemplateToCR(row, cr)
			return nil
		},
		New: func(name, namespace string) *agentv1alpha1.SandboxTemplate {
			return &agentv1alpha1.SandboxTemplate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:    "name",
		KeyOf:  func(row model.SandboxTemplateM) string { return row.Name },
		NameOf: func(row model.SandboxTemplateM) string { return row.Name },
		Column: sandboxTemplateColumn,
		Namespaces: rowNamespaces(namespace, list,
			func(row model.SandboxTemplateM) string { return rowNamespace(row.Namespace, namespace) }),
	})}
}

// sandboxTemplateFromCR maps one object onto the row the runtime reads.
func sandboxTemplateFromCR(cr *agentv1alpha1.SandboxTemplate) model.SandboxTemplateM {
	row := model.SandboxTemplateM{
		Name:        cr.Name,
		DisplayName: cr.Spec.DisplayName,
		Description: cr.Spec.Description,
		Status:      statusOf(cr.Spec.Enabled),
		Image:       cr.Spec.Image,
		Command:     model.StringList(cr.Spec.Command),
		Namespace:   cr.Namespace,
		Labels:      labelsFrom(cr),
		CreatedAt:   unixMilli(cr.CreationTimestamp),
		UpdatedAt:   unixMilli(cr.CreationTimestamp),
	}
	for _, item := range cr.Spec.Env {
		if item.Name == "" || item.ValueFrom != nil {
			continue
		}
		row.Env = append(row.Env, item.Name+"="+item.Value)
	}
	if cr.Spec.TTL != nil {
		row.TTLSeconds = int64(cr.Spec.TTL.Duration.Seconds())
	}
	return row
}

// SandboxTemplateToCR maps a row back onto the object the admin API writes.
func SandboxTemplateToCR(row *model.SandboxTemplateM, cr *agentv1alpha1.SandboxTemplate) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	cr.Spec.Image = row.Image
	cr.Spec.Command = []string(row.Command)
	setLabels(cr, row.Labels)
}
