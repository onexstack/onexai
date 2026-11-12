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

// runtimeProfileStore is the runtime contracts accessor over the control plane.
type runtimeProfileStore struct {
	*Store[model.RuntimeProfileM]
}

// Ensure runtimeProfileStore implements the RuntimeProfileStore interface.
var _ store.RuntimeProfileStore = (*runtimeProfileStore)(nil)

// runtimeProfileColumn maps the fields a filter may name to the row's value.
func runtimeProfileColumn(row model.RuntimeProfileM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	case "owner":
		return row.Owner, true
	default:
		return "", false
	}
}

// NewRuntimeProfileStore builds the runtime contract accessor.
func NewRuntimeProfileStore(client *declclient.Client, namespace string) *runtimeProfileStore {
	lister := client.RuntimeProfiles()
	list := func() ([]*agentv1alpha1.RuntimeProfile, error) { return lister.List(labels.Everything()) }

	return &runtimeProfileStore{Store: declcrud.NewStore(declcrud.Spec[model.RuntimeProfileM, *agentv1alpha1.RuntimeProfile]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.RuntimeProfile] {
			return client.Clientset.AgentV1alpha1().RuntimeProfiles(namespace)
		},
		GetByKey: getByKey(lister.RuntimeProfiles(namespace).Get, runtimeProfileFromCR),
		List:     list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.RuntimeProfile) (model.RuntimeProfileM, error) {
			return runtimeProfileFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.RuntimeProfileM, cr *agentv1alpha1.RuntimeProfile) error {
			RuntimeProfileToCR(row, cr)
			return nil
		},
		New: func(name, namespace string) *agentv1alpha1.RuntimeProfile {
			return &agentv1alpha1.RuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:    "name",
		KeyOf:  func(row model.RuntimeProfileM) string { return row.Name },
		NameOf: func(row model.RuntimeProfileM) string { return row.Name },
		Column: runtimeProfileColumn,
		Namespaces: rowNamespaces(namespace, list,
			func(row model.RuntimeProfileM) string { return rowNamespace(row.Namespace, namespace) }),
	})}
}

func runtimeProfileFromCR(cr *agentv1alpha1.RuntimeProfile) model.RuntimeProfileM {
	row := model.RuntimeProfileM{
		Name:             cr.Name,
		Namespace:        cr.Namespace,
		DisplayName:      cr.Spec.DisplayName,
		Description:      cr.Spec.Description,
		Status:           statusOf(cr.Spec.Enabled),
		Kind:             cr.Spec.Kind,
		Endpoint:         cr.Spec.Endpoint,
		ModelAccess:      cr.Spec.ModelAccess,
		ResolvedEndpoint: cr.Status.ResolvedEndpoint,
		Labels:           labelsFrom(cr),
		CreatedAt:        unixMilli(cr.CreationTimestamp),
		UpdatedAt:        unixMilli(cr.CreationTimestamp),
	}
	if cr.Spec.Owner != nil {
		row.Owner = cr.Spec.Owner.Name
	}
	if cr.Spec.ClientConfig != nil {
		row.ClientConfigJSON = encodeJSON(cr.Spec.ClientConfig)
		if cr.Spec.ClientConfig.URL != nil {
			row.Endpoint = *cr.Spec.ClientConfig.URL
		}
	}
	if cr.Spec.HostingRef != nil {
		row.HostingRef = cr.Spec.HostingRef.Name
	}
	if cr.Spec.Auth != nil {
		if cr.Spec.Auth.BearerTokenRef != nil {
			row.AuthBearerSecret = cr.Spec.Auth.BearerTokenRef.Name
			row.AuthBearerKey = cr.Spec.Auth.BearerTokenRef.Key
		}
		if cr.Spec.Auth.CABundleRef != nil {
			row.AuthCASecret = cr.Spec.Auth.CABundleRef.Name
			row.AuthCAKey = cr.Spec.Auth.CABundleRef.Key
		}
		row.AuthInsecureVerify = cr.Spec.Auth.InsecureSkipVerify
	}
	if cr.Spec.Limits.RunTimeout != nil {
		row.RunTimeoutNanos = int64(cr.Spec.Limits.RunTimeout.Duration)
	}
	row.MaxConcurrentRun = cr.Spec.Limits.MaxConcurrentRuns
	row.MaxToolCalls = cr.Spec.Limits.MaxToolCalls
	if b, err := json.Marshal(cr.Status.Capabilities); err == nil {
		row.Capabilities = string(b)
	}
	return row
}

// RuntimeProfileToCR writes the spec fields only: status belongs to the
// controller, and a store write that also wrote status would race it.
func RuntimeProfileToCR(row *model.RuntimeProfileM, cr *agentv1alpha1.RuntimeProfile) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	cr.Spec.Kind = row.Kind
	cr.Spec.Endpoint = row.Endpoint
	cr.Spec.ClientConfig = nil
	if row.ClientConfigJSON != "" {
		cfg := &agentv1alpha1.ClientConfig{}
		if err := json.Unmarshal([]byte(row.ClientConfigJSON), cfg); err == nil {
			cr.Spec.ClientConfig = cfg
			cr.Spec.Endpoint = ""
		}
	}
	cr.Spec.ModelAccess = row.ModelAccess
	if row.HostingRef != "" {
		cr.Spec.HostingRef = &agentv1alpha1.Reference{Name: row.HostingRef}
	} else {
		cr.Spec.HostingRef = nil
	}
	cr.Spec.Auth = nil
	if row.AuthBearerSecret != "" || row.AuthCASecret != "" || row.AuthInsecureVerify {
		cr.Spec.Auth = &agentv1alpha1.RuntimeAuth{InsecureSkipVerify: row.AuthInsecureVerify}
		if row.AuthBearerSecret != "" {
			cr.Spec.Auth.BearerTokenRef = &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: row.AuthBearerSecret},
				Key:                  row.AuthBearerKey,
			}
		}
		if row.AuthCASecret != "" {
			cr.Spec.Auth.CABundleRef = &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: row.AuthCASecret},
				Key:                  row.AuthCAKey,
			}
		}
	}
	cr.Spec.Limits = agentv1alpha1.RuntimeLimits{
		MaxConcurrentRuns: row.MaxConcurrentRun,
		MaxToolCalls:      row.MaxToolCalls,
	}
	if row.RunTimeoutNanos > 0 {
		cr.Spec.Limits.RunTimeout = &metav1.Duration{Duration: time.Duration(row.RunTimeoutNanos)}
	}
	setLabels(cr, row.Labels)
}
