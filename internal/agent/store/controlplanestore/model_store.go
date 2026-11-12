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

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// apiKeySecretKey is the key a credential Secret carries the API key under.
const apiKeySecretKey = "api-key"

// apiKeySecretName names the Secret the store writes for a model's key.
func apiKeySecretName(modelName string) string { return modelName + "-api-key" }

// modelStore is the models accessor over the control plane.
type modelStore struct {
	*Store[model.ModelM]
}

// Ensure modelStore implements the ModelStore interface.
var _ store.ModelStore = (*modelStore)(nil)

// modelColumn maps the fields a filter may name to the row's value.
func modelColumn(row model.ModelM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	case "namespace":
		return row.Namespace, true
	case "owner":
		return row.Owner, true
	default:
		return "", false
	}
}

// NewModelStore builds the model accessor.
//
// The API key is read from the model's credentialRef, not stored on the object:
// the row shape the runtime consumes keeps one field for it, and this store
// fills that field from the Secret on the way out. OpenAPIKey treats a value with
// no cipher prefix as plaintext, so the runtime's own key handling needs no
// change.
func NewModelStore(client *declclient.Client, namespace string) *modelStore {
	lister := client.Models()
	secrets := newSecretReader(client.Secrets())

	// load reads one endpoint, including the credential Secret it references.
	// The namespace is a parameter rather than the store's configuration
	// because a cross-namespace reference must read the model's own Secret.
	load := func(ctx context.Context, ns, name string) (model.ModelM, bool, error) {
		item, err := lister.Models(ns).Get(name)
		if err != nil {
			if isNotFound(err) {
				return model.ModelM{}, false, nil
			}
			return model.ModelM{}, false, err
		}
		row := modelFromCR(item)
		if item.Spec.CredentialRef != nil {
			key, err := secrets.get(ctx, item.Namespace, item.Spec.CredentialRef.Name, item.Spec.CredentialRef.Key)
			if err != nil {
				return model.ModelM{}, false, err
			}
			row.APIKey = key
		}
		return row, true, nil
	}

	return &modelStore{Store: &Store[model.ModelM]{
		Reader: Reader[model.ModelM]{
			Key:                "name",
			KeyOf:              func(row model.ModelM) string { return row.Name },
			GetByKey:           func(ctx context.Context, name string) (model.ModelM, bool, error) { return load(ctx, namespace, name) },
			GetByNamespacedKey: load,
			All: func(ctx context.Context) ([]model.ModelM, error) {
				items, err := lister.List(labels.Everything())
				if err != nil {
					return nil, err
				}
				rows := make([]model.ModelM, 0, len(items))
				for _, item := range items {
					row := modelFromCR(item)
					if item.Spec.CredentialRef != nil {
						key, err := secrets.get(ctx, item.Namespace, item.Spec.CredentialRef.Name, item.Spec.CredentialRef.Key)
						if err != nil {
							return nil, err
						}
						row.APIKey = key
					}
					rows = append(rows, row)
				}
				return rows, nil
			},
			Column: modelColumn,
		},
		CreateFunc: func(ctx context.Context, row *model.ModelM) error {
			ns := rowNamespace(row.Namespace, namespace)
			cr := &agentv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: row.Name, Namespace: ns}}
			ModelToCR(row, cr)
			created, err := client.Clientset.AgentV1alpha1().Models(ns).Create(ctx, cr, metav1.CreateOptions{})
			if err != nil {
				return err
			}
			return writeModelSecret(ctx, client, created, row)
		},
		UpdateFunc: func(ctx context.Context, row *model.ModelM) error {
			ns, err := resolveNamespace(
				func() ([]*agentv1alpha1.Model, error) { return lister.List(labels.Everything()) },
				row.Name, rowNamespace(row.Namespace, namespace))
			if err != nil {
				return err
			}
			api := client.Clientset.AgentV1alpha1().Models(ns)
			cr, err := api.Get(ctx, row.Name, metav1.GetOptions{}) //nolint:kubelistcheck // must not read a stale cache
			if err != nil {
				return err
			}
			ModelToCR(row, cr)
			updated, err := api.Update(ctx, cr, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			return writeModelSecret(ctx, client, updated, row)
		},
		DeleteFunc: func(ctx context.Context, name string) error {
			// Deleting something already gone is success; a retried delete must not
			// surface as an error. An ambiguous name is refused before the call.
			ns, err := resolveNamespace(
				func() ([]*agentv1alpha1.Model, error) { return lister.List(labels.Everything()) },
				name, writeNamespace(namespace))
			if err != nil {
				return err
			}
			return ignoreNotFound(client.Clientset.AgentV1alpha1().Models(ns).Delete(ctx, name, metav1.DeleteOptions{}))
		},
	}}
}

// writeModelSecret publishes the API key the object references. The Secret is
// owned by the model, so deleting the model reclaims the credential with it.
func writeModelSecret(ctx context.Context, client *declclient.Client, cr *agentv1alpha1.Model, row *model.ModelM) error {
	if row.APIKey == "" {
		return nil
	}
	return writeSecret(ctx, client.KubeClient, cr.Namespace, apiKeySecretName(cr.Name),
		map[string][]byte{apiKeySecretKey: []byte(row.APIKey)}, "Model", cr.Name, cr.UID)
}

func modelFromCR(cr *agentv1alpha1.Model) model.ModelM {
	row := model.ModelM{
		Namespace:    cr.Namespace,
		Name:         cr.Name,
		DisplayName:  cr.Spec.DisplayName,
		Description:  cr.Spec.Description,
		Status:       statusOf(cr.Spec.Enabled),
		Owner:        subjectName(cr.Spec.Owner),
		Provider:     cr.Spec.Provider,
		BaseURL:      cr.Spec.Endpoint,
		Model:        cr.Spec.Model,
		Capabilities: model.StringList(cr.Spec.Capabilities),
		Labels:       labelsFrom(cr),
		CreatedAt:    unixMilli(cr.CreationTimestamp),
		UpdatedAt:    unixMilli(cr.CreationTimestamp),
	}
	// Sampling parameters have no column in the row shape; the runtime reads them
	// from the request or the deployment default, so they are not carried here.
	return row
}

func ModelToCR(row *model.ModelM, cr *agentv1alpha1.Model) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	cr.Spec.Provider = row.Provider
	cr.Spec.Endpoint = row.BaseURL
	cr.Spec.Model = row.Model
	cr.Spec.Capabilities = []string(row.Capabilities)
	cr.Spec.CredentialRef = nil
	if row.APIKey != "" {
		cr.Spec.CredentialRef = &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: apiKeySecretName(row.Name)},
			Key:                  apiKeySecretKey,
		}
	}
	if row.Owner != "" {
		cr.Spec.Owner = &rbacv1.Subject{Kind: rbacv1.UserKind, Name: row.Owner}
	}
	setLabels(cr, row.Labels)
}

// headersJSON encodes a header map the way the row column holds it.
func headersJSON(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	encoded, err := json.Marshal(headers)
	if err != nil {
		return ""
	}
	return string(encoded)
}
