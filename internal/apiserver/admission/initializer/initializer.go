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

// Package initializer hands the clientsets an admission plugin needs to the
// plugins that ask for them.
//
// Upstream's admissioninitializer only knows about the built-in Kubernetes
// clients. onex's own API groups are served by the same apiserver but have a
// generated clientset, so a plugin that reads Agents (for example) needs its
// own Wants interface and its own initializer.
package initializer

import (
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/client-go/kubernetes"

	versioned "github.com/onexstack/onexai/pkg/generated/clientset/versioned"
)

// WantsOnexClientSet is implemented by plugins that read onex API objects.
type WantsOnexClientSet interface {
	admission.InitializationValidator
	SetOnexClientSet(versioned.Interface)
}

// WantsKubeClientSet is implemented by plugins that read core Kubernetes
// objects (namespaces, for instance) through the loopback.
type WantsKubeClientSet interface {
	admission.InitializationValidator
	SetKubeClientSet(kubernetes.Interface)
}

// NewPluginInitializer returns an initializer that injects both clientsets.
func NewPluginInitializer(onex versioned.Interface, kube kubernetes.Interface) admission.PluginInitializer {
	return &pluginInitializer{onex: onex, kube: kube}
}

type pluginInitializer struct {
	onex versioned.Interface
	kube kubernetes.Interface
}

// Initialize implements admission.PluginInitializer.
func (i *pluginInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantsOnexClientSet); ok {
		wants.SetOnexClientSet(i.onex)
	}

	if wants, ok := plugin.(WantsKubeClientSet); ok {
		wants.SetKubeClientSet(i.kube)
	}
}
