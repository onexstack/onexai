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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// BotApp is one Lark application the bot serves. Its credentials live in a
// Secret; the event subscription itself is operated in the Lark console, so the
// status reports what this platform sees rather than reconciling Lark.
//
// Deprecated: no runtime reads BotApp. The chat service takes its single Lark
// application from its service configuration (pkg/options/lark_options.go).
// The type remains served only so an existing object can be inspected.
type BotApp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec BotAppSpec `json:"spec,omitempty"`
	// +optional
	Status BotAppStatus `json:"status,omitempty"`
}

// BotAppSpec defines the desired state of BotApp.
type BotAppSpec struct {
	CommonSpec `json:",inline"`

	// AppID is the Lark application id.
	AppID string `json:"appID"`

	// CredentialRef names the Secret key holding the application secret.
	CredentialRef corev1.SecretKeySelector `json:"credentialRef"`

	// EventMode is how events arrive: webhook or websocket.
	// +optional
	EventMode string `json:"eventMode,omitempty"`

	// BotName is how the bot introduces itself.
	// +optional
	BotName string `json:"botName,omitempty"`

	// AllowFrom lists the users allowed to talk to the bot; empty means anyone.
	// +optional
	AllowFrom []string `json:"allowFrom,omitempty"`

	// AllowedChats lists the chats the bot answers in; empty means any.
	// +optional
	AllowedChats []string `json:"allowedChats,omitempty"`

	// GroupOnly ignores direct messages.
	// +optional
	GroupOnly bool `json:"groupOnly,omitempty"`

	// ReplyToTrigger answers only messages that name the bot.
	// +optional
	ReplyToTrigger bool `json:"replyToTrigger,omitempty"`

	// ThreadIsolation keeps each Lark thread a separate conversation.
	// +optional
	ThreadIsolation bool `json:"threadIsolation,omitempty"`

	// ImageBatchWindowSeconds groups consecutive images into one message.
	// +optional
	ImageBatchWindowSeconds int32 `json:"imageBatchWindowSeconds,omitempty"`
}

// BotAppStatus defines the observed state of BotApp.
type BotAppStatus struct {
	CommonStatus `json:",inline"`

	// WebhookURL is the endpoint Lark was configured to call, when the webhook
	// mode is in use.
	// +optional
	WebhookURL string `json:"webhookURL,omitempty"`
}

// BotApp condition types beyond the shared ones.
const (
	// ConditionSubscribed reports that the application's event subscription was
	// last seen working.
	ConditionSubscribed = "Subscribed"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// BotAppList is a list of BotApp objects.
type BotAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []BotApp `json:"items"`
}
