// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package chat

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// BotApp is the internal (hub) schema for the botapps API.
//
// Deprecated: see the v1alpha1 type. chat does not read BotApp.
type BotApp struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   BotAppSpec
	Status BotAppStatus
}

// BotAppSpec defines the desired state of BotApp.
type BotAppSpec struct {
	CommonSpec

	AppID                   string
	CredentialRef           corev1.SecretKeySelector
	EventMode               string
	BotName                 string
	AllowFrom               []string
	AllowedChats            []string
	GroupOnly               bool
	ReplyToTrigger          bool
	ThreadIsolation         bool
	ImageBatchWindowSeconds int32
}

// BotAppStatus defines the observed state of BotApp.
type BotAppStatus struct {
	CommonStatus

	WebhookURL string
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// BotAppList is a list of BotApp objects.
type BotAppList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []BotApp
}
