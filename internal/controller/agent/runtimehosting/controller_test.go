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

package runtimehosting

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func TestInstanceNameIsDeterministic(t *testing.T) {
	assert.Equal(t, instanceName("pool", "agent"), instanceName("pool", "agent"))
	assert.NotEqual(t, instanceName("pool", "agent-a"), instanceName("pool", "agent-b"))
}

func TestProcessEnvCarriesIdentity(t *testing.T) {
	hosting := &agentv1alpha1.RuntimeHosting{
		Spec: agentv1alpha1.RuntimeHostingSpec{
			ProfileRef: agentv1alpha1.Reference{Name: "kagent"},
			Mode:       agentv1alpha1.HostingModeProcess,
		},
	}
	env := strings.Join(runtimeEnv(hosting, "agent", 18080), "\n")
	assert.Contains(t, env, "ONEXAI_RUNTIME_PROFILE=kagent")
	assert.Contains(t, env, "ONEXAI_AGENT_NAME=agent")
	assert.Contains(t, env, "ONEXAI_RUNTIME_PORT=18080")
	assert.Contains(t, env, "ONEXAI_CAPABILITY_URL=http://127.0.0.1:8091/v1/mcp")
}

func TestDockerEnvUsesHostAlias(t *testing.T) {
	hosting := &agentv1alpha1.RuntimeHosting{
		Spec: agentv1alpha1.RuntimeHostingSpec{
			ProfileRef: agentv1alpha1.Reference{Name: "kagent"},
			Mode:       agentv1alpha1.HostingModeDocker,
		},
	}
	env := strings.Join(runtimeEnv(hosting, "agent", 18080), "\n")
	assert.Contains(t, env, "ONEXAI_CAPABILITY_URL=http://host.docker.internal:8091/v1/mcp")
}

func TestHealthyIsFalseForAClosedPort(t *testing.T) {
	assert.False(t, healthy("http://127.0.0.1:1"))
}
