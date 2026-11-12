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

package conversion

import (
	"encoding/json"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// WebhookV1ToWebhookModel converts a proto run-lifecycle hook to the domain
// model.
func WebhookV1ToWebhookModel(w *v1.Webhook) *model.WebhookM {
	if w == nil {
		return &model.WebhookM{}
	}
	out := &model.WebhookM{
		Name:           w.GetName(),
		Status:         w.GetStatus(),
		Type:           w.GetType(),
		URL:            w.GetUrl(),
		SecretRef:      w.GetSecretRef(),
		AuthToken:      w.GetAuthToken(),
		Events:         w.GetEvents(),
		Audiences:      w.GetAudiences(),
		TimeoutSeconds: int(w.GetTimeoutSeconds()),
		FailureMode:    w.GetFailureMode(),
		Headers:        w.GetHeaders(),
		Template:       w.GetTemplate(),
		Owner:          w.GetOwner(),
		CreatedAt:      w.GetCreatedAt(),
		UpdatedAt:      w.GetUpdatedAt(),
	}
	if cc := w.GetClientConfig(); cc != nil {
		cfg := ClientConfigV1ToCR(cc)
		out.ClientConfigJSON = jsonText(cfg)
		if cfg.URL != nil {
			out.URL = *cfg.URL
		}
		if cfg.TimeoutSeconds > 0 {
			out.TimeoutSeconds = int(cfg.TimeoutSeconds)
		}
		if len(cfg.Headers) > 0 {
			out.Headers = mergeHeaders(out.Headers, cfg.Headers)
		}
		// clientConfig.secretRef is not part of the Webhook contract anymore:
		// the top-level secretRef is the only credential reference. Merging it
		// here would let a stale nested value override the one the operator
		// just submitted.
	}
	return out
}

// WebhookModelToWebhookV1 converts the domain model back to the wire shape.
func WebhookModelToWebhookV1(m *model.WebhookM) *v1.Webhook {
	if m == nil {
		return nil
	}
	out := &v1.Webhook{
		Name:           m.Name,
		Status:         StatusToV1(m.Status),
		Type:           m.Type,
		Url:            m.URL,
		SecretRef:      m.SecretRef,
		Events:         m.Events,
		Audiences:      m.Audiences,
		TimeoutSeconds: int32(m.TimeoutSeconds),
		FailureMode:    m.FailureMode,
		Headers:        m.Headers,
		Template:       m.Template,
		Owner:          m.Owner,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
	if m.ClientConfigJSON != "" {
		cfg := &agentv1alpha1.ClientConfig{}
		if err := json.Unmarshal([]byte(m.ClientConfigJSON), cfg); err == nil {
			out.ClientConfig = ClientConfigFromCR(cfg)
		}
	}
	return out
}

// ClientConfigV1ToCR turns the wire message into the object that is
// persisted in the CRD. The JSON shape of that object is the Kubernetes
// WebhookClientConfig shape, which is what the runtime resolves.
func ClientConfigV1ToCR(cc *v1.ClientConfig) *agentv1alpha1.ClientConfig {
	out := &agentv1alpha1.ClientConfig{
		TimeoutSeconds:        cc.GetTimeoutSeconds(),
		InsecureSkipTLSVerify: cc.GetInsecureSkipTLSVerify(),
		Headers:               cc.GetHeaders(),
	}
	out.CABundle = cc.GetCaBundle()
	if cc.GetUrl() != "" {
		out.URL = strPtr(cc.GetUrl())
	}
	if svc := cc.GetService(); svc != nil {
		out.Service = &admissionregistrationv1.ServiceReference{
			Namespace: svc.GetNamespace(),
			Name:      svc.GetName(),
		}
		if svc.GetPort() != 0 {
			out.Service.Port = int32Ptr(svc.GetPort())
		}
		if svc.GetPath() != "" {
			out.Service.Path = strPtr(svc.GetPath())
		}
	}
	if cc.GetSecretRef() != "" {
		out.SecretRef = &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: cc.GetSecretRef()},
			Key:                  cc.GetSecretKey(),
		}
	}
	if cc.GetHeadersRef() != "" {
		out.HeadersRef = &corev1.LocalObjectReference{Name: cc.GetHeadersRef()}
	}
	return out
}

func ClientConfigFromCR(cfg *agentv1alpha1.ClientConfig) *v1.ClientConfig {
	out := &v1.ClientConfig{
		CaBundle:              cfg.CABundle,
		TimeoutSeconds:        cfg.TimeoutSeconds,
		InsecureSkipTLSVerify: cfg.InsecureSkipTLSVerify,
		Headers:               cfg.Headers,
	}
	if cfg.URL != nil {
		out.Url = *cfg.URL
	}
	if cfg.Service != nil {
		out.Service = &v1.ServiceReference{
			Namespace: cfg.Service.Namespace,
			Name:      cfg.Service.Name,
		}
		if cfg.Service.Port != nil {
			out.Service.Port = *cfg.Service.Port
		}
		if cfg.Service.Path != nil {
			out.Service.Path = *cfg.Service.Path
		}
	}
	if cfg.SecretRef != nil {
		out.SecretRef = cfg.SecretRef.Name
		out.SecretKey = cfg.SecretRef.Key
	}
	if cfg.HeadersRef != nil {
		out.HeadersRef = cfg.HeadersRef.Name
	}
	return out
}

func jsonText(v any) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil || string(data) == "null" {
		return ""
	}
	return string(data)
}

func mergeHeaders(base, override map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(override))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		out[key] = value
	}
	return out
}

func strPtr(value string) *string { return &value }

func int32Ptr(value int32) *int32 { return &value }
