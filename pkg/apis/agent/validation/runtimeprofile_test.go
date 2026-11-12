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

package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func TestValidateRuntimeProfile(t *testing.T) {
	path := field.NewPath("spec")

	t.Run("inproc", func(t *testing.T) {
		errs := ValidateRuntimeProfile(&agent.RuntimeProfileSpec{Kind: agent.RuntimeKindInproc}, path)
		assert.Empty(t, errs)
	})

	t.Run("a2a with endpoint", func(t *testing.T) {
		errs := ValidateRuntimeProfile(&agent.RuntimeProfileSpec{
			Kind: agent.RuntimeKindA2A, Endpoint: "https://runtime:8080", ModelAccess: agent.ModelAccessProxy,
		}, path)
		assert.Empty(t, errs)
	})

	t.Run("a2a without endpoint or hosting", func(t *testing.T) {
		errs := ValidateRuntimeProfile(&agent.RuntimeProfileSpec{Kind: agent.RuntimeKindA2A}, path)
		assert.NotEmpty(t, errs)
	})

	t.Run("endpoint and hosting are exclusive", func(t *testing.T) {
		errs := ValidateRuntimeProfile(&agent.RuntimeProfileSpec{
			Kind: agent.RuntimeKindA2A, Endpoint: "https://runtime:8080",
			HostingRef: &agent.Reference{Name: "pool"},
		}, path)
		assert.NotEmpty(t, errs)
	})

	t.Run("inproc rejects an endpoint", func(t *testing.T) {
		errs := ValidateRuntimeProfile(&agent.RuntimeProfileSpec{
			Kind: agent.RuntimeKindInproc, Endpoint: "https://runtime:8080",
		}, path)
		assert.NotEmpty(t, errs)
	})

	t.Run("unknown kind", func(t *testing.T) {
		errs := ValidateRuntimeProfile(&agent.RuntimeProfileSpec{Kind: "wasm"}, path)
		assert.NotEmpty(t, errs)
	})
}

func TestValidateRuntimeHosting(t *testing.T) {
	path := field.NewPath("spec")

	t.Run("docker needs a digest", func(t *testing.T) {
		errs := ValidateRuntimeHosting(&agent.RuntimeHostingSpec{
			ProfileRef: agent.Reference{Name: "kagent"}, Mode: agent.HostingModeDocker,
			Image: "registry/kagent:latest",
		}, path)
		assert.NotEmpty(t, errs)
	})

	t.Run("digest pinned is accepted", func(t *testing.T) {
		errs := ValidateRuntimeHosting(&agent.RuntimeHostingSpec{
			ProfileRef: agent.Reference{Name: "kagent"}, Mode: agent.HostingModeDocker,
			Image: "registry/kagent@sha256:" + repeat('a', 64),
		}, path)
		assert.Empty(t, errs)
	})

	t.Run("process needs a command", func(t *testing.T) {
		errs := ValidateRuntimeHosting(&agent.RuntimeHostingSpec{
			ProfileRef: agent.Reference{Name: "kagent"}, Mode: agent.HostingModeProcess,
		}, path)
		assert.NotEmpty(t, errs)
	})
}

func repeat(ch byte, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = ch
	}
	return string(out)
}

func TestValidateAgentOutputSchema(t *testing.T) {
	path := field.NewPath("spec")
	valid := &agent.AgentSpec{OutputSchema: &apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"status":{"type":"string"}}}`)}}
	assert.Empty(t, ValidateAgent(valid, path))

	invalid := &agent.AgentSpec{OutputSchema: &apiextensionsv1.JSON{Raw: []byte(`{"type":"array"}`)}}
	assert.NotEmpty(t, ValidateAgent(invalid, path))
}

func TestValidateAgentMemoryRef(t *testing.T) {
	path := field.NewPath("spec")
	empty := &agent.AgentSpec{MemoryRef: &agent.Reference{}}
	assert.NotEmpty(t, ValidateAgent(empty, path))

	valid := &agent.AgentSpec{MemoryRef: &agent.Reference{Name: "team-memory"}}
	assert.Empty(t, ValidateAgent(valid, path))
}
