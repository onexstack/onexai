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

// skillStore is the skills accessor over the control plane.
type skillStore struct {
	*Store[model.SkillM]
}

// Ensure skillStore implements the SkillStore interface.
var _ store.SkillStore = (*skillStore)(nil)

// NewSkillStore builds the skills accessor.
func NewSkillStore(client *declclient.Client, namespace string) *skillStore {
	lister := client.Skills()
	list := func() ([]*agentv1alpha1.Skill, error) { return lister.List(labels.Everything()) }

	return &skillStore{Store: declcrud.NewStore(declcrud.Spec[model.SkillM, *agentv1alpha1.Skill]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.Skill] {
			return client.Clientset.AgentV1alpha1().Skills(namespace)
		},
		GetByKey: getByKey(lister.Skills(namespace).Get, skillFromCR),
		GetByNamespacedKey: getByNamespacedKey(func(ns, name string) (*agentv1alpha1.Skill, error) {
			return lister.Skills(ns).Get(name)
		}, skillFromCR),
		List: list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.Skill) (model.SkillM, error) {
			return skillFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.SkillM, cr *agentv1alpha1.Skill) error {
			SkillToCR(row, cr)
			return nil
		},
		New: func(name, namespace string) *agentv1alpha1.Skill {
			return &agentv1alpha1.Skill{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:    "name",
		KeyOf:  func(row model.SkillM) string { return row.Name },
		NameOf: func(row model.SkillM) string { return row.Name },
		Column: skillColumn,
		Namespaces: rowNamespaces(namespace, list,
			func(row model.SkillM) string { return rowNamespace(row.Namespace, namespace) }),
	})}
}

// skillColumn reads a filterable column off a skill row.
func skillColumn(row model.SkillM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	case "workspace", "namespace":
		return row.Namespace, true
	default:
		return "", false
	}
}
