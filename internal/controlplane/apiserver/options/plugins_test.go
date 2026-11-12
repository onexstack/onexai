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

package options

import (
	"testing"

	"k8s.io/apiserver/pkg/admission"

	"github.com/onexstack/onexai/internal/apiserver/admission/plugin/agentreference"
)

// A plugin that is registered but missing from AllOrderedPlugins is never run,
// and one missing from the default-on set is silently off. Both mistakes are
// invisible until someone relies on the protection.
func TestAgentReferenceProtectionIsRegisteredAndOnByDefault(t *testing.T) {
	plugins := admission.NewPlugins()
	RegisterAllAdmissionPlugins(plugins)

	if !contains(plugins.Registered(), agentreference.PluginName) {
		t.Fatalf("%s is not registered", agentreference.PluginName)
	}

	if !contains(AllOrderedPlugins, agentreference.PluginName) {
		t.Fatalf("%s is missing from AllOrderedPlugins", agentreference.PluginName)
	}

	if DefaultOffAdmissionPlugins().Has(agentreference.PluginName) {
		t.Fatalf("%s is off by default", agentreference.PluginName)
	}
}

// Every ordered plugin must be registered, or the apiserver refuses to start.
func TestEveryOrderedPluginIsRegistered(t *testing.T) {
	plugins := admission.NewPlugins()
	RegisterAllAdmissionPlugins(plugins)

	registered := plugins.Registered()
	for _, name := range AllOrderedPlugins {
		if !contains(registered, name) {
			t.Errorf("%s is in AllOrderedPlugins but not registered", name)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
