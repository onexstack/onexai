// Copyright 2022 Lingfei Kong <colin404@foxmail.com>
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
//
// The original repo for this file is https://github.com/onexstack/onex.
//

//nolint:staticcheck
package options

// This file exists to force the desired plugin implementations to be linked.
// This should probably be part of some configuration fed into the build for a
// given binary target.
import (
	// Admission policies.

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/namespace/lifecycle"
	mutatingadmissionpolicy "k8s.io/apiserver/pkg/admission/plugin/policy/mutating"
	validatingadmissionpolicy "k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	mutatingwebhook "k8s.io/apiserver/pkg/admission/plugin/webhook/mutating"
	validatingwebhook "k8s.io/apiserver/pkg/admission/plugin/webhook/validating"
	"k8s.io/kubernetes/plugin/pkg/admission/admit"
	"k8s.io/kubernetes/plugin/pkg/admission/deny"
	"k8s.io/kubernetes/plugin/pkg/admission/namespace/autoprovision"
	"k8s.io/kubernetes/plugin/pkg/admission/namespace/exists"

	"github.com/onexstack/onexai/internal/apiserver/admission/plugin/agentreference"
)

// AllOrderedPlugins is the list of all the plugins in order.
var AllOrderedPlugins = []string{
	admit.PluginName,         // AlwaysAdmit
	autoprovision.PluginName, // NamespaceAutoProvision
	lifecycle.PluginName,     // NamespaceLifecycle
	exists.PluginName,        // NamespaceExists

	// new admission plugins should generally be inserted above here
	// webhook, resourcequota, and deny plugins must go at the end

	// AgentReferenceProtection refuses deletes that would break an enabled
	// Agent, which no per-object strategy can see.
	agentreference.PluginName,

	// In-process CEL policies run before the outbound webhooks, matching
	// kube-apiserver: a policy that only needs the object in hand never pays for
	// a network round trip, and a rejecting policy stops the request before any
	// webhook sees it.
	mutatingadmissionpolicy.PluginName,   // MutatingAdmissionPolicy
	mutatingwebhook.PluginName,           // MutatingAdmissionWebhook
	validatingadmissionpolicy.PluginName, // ValidatingAdmissionPolicy
	validatingwebhook.PluginName,         // ValidatingAdmissionWebhook
	// resourcequota.PluginName, // ResourceQuota
	deny.PluginName, // AlwaysDeny
}

// RegisterAllAdmissionPlugins registers all admission plugins.
// The order of registration is irrelevant, see AllOrderedPlugins for execution order.
func RegisterAllAdmissionPlugins(plugins *admission.Plugins) {
	admit.Register(plugins) // DEPRECATED as no real meaning
	autoprovision.Register(plugins)
	lifecycle.Register(plugins)
	exists.Register(plugins)
	agentreference.Register(plugins)
	mutatingadmissionpolicy.Register(plugins)
	mutatingwebhook.Register(plugins)
	validatingadmissionpolicy.Register(plugins)
	validatingwebhook.Register(plugins)
	deny.Register(plugins) // DEPRECATED as no real meaning
}

// DefaultOffAdmissionPlugins get admission plugins off by default, mirroring
// kube-apiserver: NamespaceLifecycle, the two webhook plugins and
// AgentReferenceProtection are on, while NamespaceAutoProvision and
// NamespaceExists stay off. Auto-provisioning a namespace on a typo silently
// hides mistakes, so it must be opted into explicitly with
// --enable-admission-plugins=NamespaceAutoProvision.
func DefaultOffAdmissionPlugins() sets.Set[string] {
	defaultOnPlugins := sets.New(
		lifecycle.PluginName, // NamespaceLifecycle

		// CEL policies are on by default: this server is the platform's
		// declarative extension point, and a ValidatingAdmissionPolicy with no
		// binding is a no-op fast path. Both feature gates are GA.
		mutatingadmissionpolicy.PluginName,   // MutatingAdmissionPolicy
		mutatingwebhook.PluginName,           // MutatingAdmissionWebhook
		validatingadmissionpolicy.PluginName, // ValidatingAdmissionPolicy
		validatingwebhook.PluginName,         // ValidatingAdmissionWebhook

		// On by default: a delete that silently breaks an Agent is a mistake
		// made at write time, and the error names the agents in the way.
		// --disable-admission-plugins=AgentReferenceProtection opts out.
		agentreference.PluginName,
	)

	return sets.New(AllOrderedPlugins...).Difference(defaultOnPlugins)
}
