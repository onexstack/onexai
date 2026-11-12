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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Command is the control-plane record of a platform command.
//
// The command bodies are compiled into the chat server: a command is code, not
// data. This object is its catalogue entry — the name a binding writes, the
// group and labels an operator selects on, and the usage text a console shows.
// The chat server reconciles one object per built-in command at start-up; a
// command exists here because the binary implements it, not because someone
// wrote an object.
//
// User-defined commands are a different resource: CustomCommand, whose body is
// declared and executed by the chat server.
type Command struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the catalogue entry.
	// +optional
	Spec CommandSpec `json:"spec,omitempty"`

	// Status is reserved; there is no controller for this resource.
	// +optional
	Status CommonStatus `json:"status,omitempty"`
}

// CommandSpec describes one platform command.
type CommandSpec struct {
	CommonSpec `json:",inline"`

	// Group is the catalogue group key the command is listed under, e.g. "duty"
	// or "job". It is the same key a binding selects with the group label.
	Group string `json:"group"`

	// Aliases are extra names the command answers to.
	// +optional
	Aliases []string `json:"aliases,omitempty"`

	// Usage is the human-facing invocation shape, e.g. "<service> [--env=prod]".
	// +optional
	Usage string `json:"usage,omitempty"`

	// Source records who provides the implementation. "builtin" means the chat
	// binary implements it; the object is a projection of the compiled catalog.
	// +optional
	Source string `json:"source,omitempty"`

	// AccountLevel marks a command that acts on the caller's account rather than
	// in a conversation, and is therefore granted unconditionally.
	// +optional
	AccountLevel bool `json:"accountLevel,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CommandList is a list of Command objects.
type CommandList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Command `json:"items"`
}
