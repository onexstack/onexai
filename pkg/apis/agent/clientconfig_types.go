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

package agent

import (
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
)

// ClientConfig is the internal (hub) form of the platform's external endpoint
// configuration. It mirrors the versioned type in v1alpha1; the conversion is
// structural because both embed the same Kubernetes WebhookClientConfig.
type ClientConfig struct {
	admissionregistrationv1.WebhookClientConfig

	TimeoutSeconds        int32
	InsecureSkipTLSVerify bool
	Headers               map[string]string
	SecretRef             *corev1.SecretKeySelector
	HeadersRef            *corev1.LocalObjectReference
}
