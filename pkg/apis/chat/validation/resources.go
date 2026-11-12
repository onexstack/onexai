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

package validation

import (
	"k8s.io/apimachinery/pkg/util/validation/field"

	chat "github.com/onexstack/onexai/pkg/apis/chat"
)

// KnownEventModes are the ways a bot receives Lark events.
var KnownEventModes = []string{"webhook", "websocket"}

// ValidateDutyGroup checks an on-call group.
func ValidateDutyGroup(spec *chat.DutyGroupSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	// An empty list is legal: it means this group contributes no agent to
	// `@oncall`, which the resolver already treats as "fall through to the next
	// reference" (see the oncall decider). Four of the five groups the migration
	// brought over are exactly that — a directory entry that exists so a ticket
	// has a group, not so it has a default agent — and refusing them would have
	// made them un-migratable.
	for i, agent := range spec.Agents {
		if agent == "" {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("agents").Index(i), agent, "must not be empty"))
		}
	}
	if spec.Order < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("order"), spec.Order, "must not be negative"))
	}
	if spec.ScheduleRef != nil && spec.ScheduleRef.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("scheduleRef", "name"),
			"scheduleRef must name a schedule"))
	}
	return allErrs
}

// ValidatePeerBinding checks a chat-peer binding.
func ValidatePeerBinding(spec *chat.PeerBindingSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if spec.Backend == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("backend"),
			"a binding must name the caller class that produced peerID"))
	}
	if spec.PeerID == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("peerID"),
			"a binding must name the chat platform's peer id"))
	}
	return allErrs
}

// ValidateBotApp checks a Lark application.
func ValidateBotApp(spec *chat.BotAppSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if spec.AppID == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("appID"), "a bot application must name its app id"))
	}
	if spec.CredentialRef.Name == "" || spec.CredentialRef.Key == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("credentialRef"),
			"credentialRef must name a Secret and a key"))
	}
	if spec.EventMode != "" {
		known := false
		for _, mode := range KnownEventModes {
			if mode == spec.EventMode {
				known = true
				break
			}
		}
		if !known {
			allErrs = append(allErrs, field.NotSupported(fldPath.Child("eventMode"), spec.EventMode, KnownEventModes))
		}
	}
	if spec.ImageBatchWindowSeconds < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("imageBatchWindowSeconds"),
			spec.ImageBatchWindowSeconds, "must not be negative"))
	}
	return allErrs
}
