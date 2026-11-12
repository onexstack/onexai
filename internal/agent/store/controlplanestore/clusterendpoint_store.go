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

// clusterEndpointStore is the cluster-endpoints accessor over the control plane.
type clusterEndpointStore struct {
	*Store[model.ClusterEndpointM]
}

var _ store.ClusterEndpointStore = (*clusterEndpointStore)(nil)

// NewClusterEndpointStore builds the cluster-endpoints accessor.
func NewClusterEndpointStore(client *declclient.Client, namespace string) *clusterEndpointStore {
	lister := client.ClusterEndpoints()
	list := func() ([]*agentv1alpha1.Clusterendpoint, error) { return lister.List(labels.Everything()) }

	return &clusterEndpointStore{Store: declcrud.NewStore(declcrud.Spec[model.ClusterEndpointM, *agentv1alpha1.Clusterendpoint]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.Clusterendpoint] {
			return client.Clientset.AgentV1alpha1().Clusterendpoints(namespace)
		},
		GetByKey: getByKey(lister.Clusterendpoints(namespace).Get, clusterEndpointFromCR),
		List:     list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.Clusterendpoint) (model.ClusterEndpointM, error) {
			return clusterEndpointFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.ClusterEndpointM, cr *agentv1alpha1.Clusterendpoint) error {
			ClusterEndpointToCR(row, cr)
			return nil
		},
		New: func(name, namespace string) *agentv1alpha1.Clusterendpoint {
			return &agentv1alpha1.Clusterendpoint{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:    "name",
		KeyOf:  func(row model.ClusterEndpointM) string { return row.Name },
		NameOf: func(row model.ClusterEndpointM) string { return row.Name },
		Column: clusterEndpointColumn,
		Namespaces: rowNamespaces(namespace, list,
			func(row model.ClusterEndpointM) string { return rowNamespace(row.Namespace, namespace) }),
	})}
}

func clusterEndpointColumn(row model.ClusterEndpointM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	case "workspace", "namespace":
		return row.Namespace, true
	case "cluster":
		return row.ClusterName, true
	default:
		return "", false
	}
}

func clusterEndpointFromCR(cr *agentv1alpha1.Clusterendpoint) model.ClusterEndpointM {
	return model.ClusterEndpointM{
		Name:        cr.Name,
		Namespace:   cr.Namespace,
		DisplayName: cr.Spec.DisplayName,
		Description: cr.Spec.Description,
		Status:      statusOf(cr.Spec.Enabled),
		ClusterName: cr.Spec.ClusterName,
		AccountID:   cr.Spec.AccountID,
		Protocol:    cr.Spec.Protocol,
		Endpoint:    cr.Spec.Endpoint,
		PrimaryMCP:  cr.Spec.PrimaryMCP,
		Labels:      labelsFrom(cr),
		CreatedAt:   unixMilli(cr.CreationTimestamp),
		UpdatedAt:   unixMilli(cr.CreationTimestamp),
	}
}

func ClusterEndpointToCR(row *model.ClusterEndpointM, cr *agentv1alpha1.Clusterendpoint) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	cr.Spec.ClusterName = row.ClusterName
	cr.Spec.AccountID = row.AccountID
	cr.Spec.Protocol = row.Protocol
	cr.Spec.Endpoint = row.Endpoint
	cr.Spec.PrimaryMCP = row.PrimaryMCP
	setLabels(cr, row.Labels)
}
