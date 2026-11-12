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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/declcrud"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// runtimeHostingStore is the runtime hosting pools accessor over the control
// plane.
type runtimeHostingStore struct {
	*Store[model.RuntimeHostingM]
}

// Ensure runtimeHostingStore implements the RuntimeHostingStore interface.
var _ store.RuntimeHostingStore = (*runtimeHostingStore)(nil)

// runtimeHostingColumn maps the fields a filter may name to the row's value.
func runtimeHostingColumn(row model.RuntimeHostingM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	case "owner":
		return row.Owner, true
	default:
		return "", false
	}
}

// NewRuntimeHostingStore builds the hosting pool accessor.
func NewRuntimeHostingStore(client *declclient.Client, namespace string) *runtimeHostingStore {
	lister := client.RuntimeHostings()
	list := func() ([]*agentv1alpha1.RuntimeHosting, error) { return lister.List(labels.Everything()) }

	return &runtimeHostingStore{Store: declcrud.NewStore(declcrud.Spec[model.RuntimeHostingM, *agentv1alpha1.RuntimeHosting]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.RuntimeHosting] {
			return client.Clientset.AgentV1alpha1().RuntimeHostings(namespace)
		},
		GetByKey: getByKey(lister.RuntimeHostings(namespace).Get, runtimeHostingFromCR),
		List:     list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.RuntimeHosting) (model.RuntimeHostingM, error) {
			return runtimeHostingFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.RuntimeHostingM, cr *agentv1alpha1.RuntimeHosting) error {
			RuntimeHostingToCR(row, cr)
			return nil
		},
		New: func(name, namespace string) *agentv1alpha1.RuntimeHosting {
			return &agentv1alpha1.RuntimeHosting{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:    "name",
		KeyOf:  func(row model.RuntimeHostingM) string { return row.Name },
		NameOf: func(row model.RuntimeHostingM) string { return row.Name },
		Column: runtimeHostingColumn,
		Namespaces: rowNamespaces(namespace, list,
			func(row model.RuntimeHostingM) string { return rowNamespace(row.Namespace, namespace) }),
	})}
}

func runtimeHostingFromCR(cr *agentv1alpha1.RuntimeHosting) model.RuntimeHostingM {
	row := model.RuntimeHostingM{
		Name:         cr.Name,
		Namespace:    cr.Namespace,
		DisplayName:  cr.Spec.DisplayName,
		Description:  cr.Spec.Description,
		Status:       statusOf(cr.Spec.Enabled),
		ProfileRef:   cr.Spec.ProfileRef.Name,
		Mode:         cr.Spec.Mode,
		Image:        cr.Spec.Image,
		Command:      append(model.StringList(nil), cr.Spec.Command...),
		MaxInstances: cr.Spec.MaxInstances,
		Labels:       labelsFrom(cr),
		CreatedAt:    unixMilli(cr.CreationTimestamp),
		UpdatedAt:    unixMilli(cr.CreationTimestamp),
	}
	if cr.Spec.Owner != nil {
		row.Owner = cr.Spec.Owner.Name
	}
	if cr.Spec.IdleTTL != nil {
		row.IdleTTLNanos = int64(cr.Spec.IdleTTL.Duration)
	}
	if b, err := json.Marshal(cr.Spec.Env); err == nil {
		row.Env = string(b)
	}
	if b, err := json.Marshal(cr.Spec.Resources); err == nil {
		row.Resources = string(b)
	}
	if b, err := json.Marshal(cr.Status.Instances); err == nil {
		row.Instances = string(b)
	}
	return row
}

// RuntimeHostingToCR writes the spec fields only; status belongs to the
// controller.
func RuntimeHostingToCR(row *model.RuntimeHostingM, cr *agentv1alpha1.RuntimeHosting) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	cr.Spec.ProfileRef = agentv1alpha1.Reference{Name: row.ProfileRef}
	cr.Spec.Mode = row.Mode
	cr.Spec.Image = row.Image
	cr.Spec.Command = append([]string(nil), row.Command...)
	cr.Spec.MaxInstances = row.MaxInstances
	if row.IdleTTLNanos > 0 {
		cr.Spec.IdleTTL = &metav1.Duration{Duration: time.Duration(row.IdleTTLNanos)}
	}
	cr.Spec.Env = nil
	if row.Env != "" {
		_ = json.Unmarshal([]byte(row.Env), &cr.Spec.Env)
	}
	cr.Spec.Resources = corev1.ResourceRequirements{}
	if row.Resources != "" {
		_ = json.Unmarshal([]byte(row.Resources), &cr.Spec.Resources)
	}
	setLabels(cr, row.Labels)
}
