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
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func validGrantSpec() agent.ReferenceGrantSpec {
	return agent.ReferenceGrantSpec{
		From: []agent.ReferenceGrantFrom{{
			Group:     "agent.onexai.io",
			Kind:      "Agent",
			Namespace: "helper-public",
		}},
		To: []agent.ReferenceGrantTo{{
			Group: "agent.onexai.io",
			Kind:  "Skill",
			Name:  "k8s-debug",
		}},
	}
}

func TestValidateReferenceGrant(t *testing.T) {
	spec := validGrantSpec()
	assert.Empty(t, ValidateReferenceGrant(&spec, field.NewPath("spec")))
}

func TestValidateReferenceGrantRequiresBothSides(t *testing.T) {
	empty := agent.ReferenceGrantSpec{}
	assert.NotEmpty(t, ValidateReferenceGrant(&empty, field.NewPath("spec")))

	noFrom := validGrantSpec()
	noFrom.From = nil
	assert.NotEmpty(t, ValidateReferenceGrant(&noFrom, field.NewPath("spec")))

	noTo := validGrantSpec()
	noTo.To = nil
	assert.NotEmpty(t, ValidateReferenceGrant(&noTo, field.NewPath("spec")))
}

func TestValidateReferenceGrantRejectsMalformedFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*agent.ReferenceGrantSpec)
	}{
		{name: "missing from group", mutate: func(s *agent.ReferenceGrantSpec) { s.From[0].Group = "" }},
		{name: "missing from kind", mutate: func(s *agent.ReferenceGrantSpec) { s.From[0].Kind = "" }},
		{name: "missing from namespace", mutate: func(s *agent.ReferenceGrantSpec) { s.From[0].Namespace = "" }},
		{name: "invalid from namespace", mutate: func(s *agent.ReferenceGrantSpec) { s.From[0].Namespace = "Not_A_Namespace" }},
		{name: "missing to group", mutate: func(s *agent.ReferenceGrantSpec) { s.To[0].Group = "" }},
		{name: "missing to kind", mutate: func(s *agent.ReferenceGrantSpec) { s.To[0].Kind = "" }},
		{name: "invalid to name", mutate: func(s *agent.ReferenceGrantSpec) { s.To[0].Name = "Not_A_Name" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := validGrantSpec()
			tc.mutate(&spec)
			assert.NotEmpty(t, ValidateReferenceGrant(&spec, field.NewPath("spec")))
		})
	}
}

func TestValidateReferenceGrantAllowsEmptyTargetName(t *testing.T) {
	spec := validGrantSpec()
	spec.To[0].Name = ""
	assert.Empty(t, ValidateReferenceGrant(&spec, field.NewPath("spec")),
		"an empty target name means every name of that kind")
}
