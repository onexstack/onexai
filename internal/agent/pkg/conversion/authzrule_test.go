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

package conversion

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func TestAuthzRuleLegacyWildcardExpandsToEverySubjectKind(t *testing.T) {
	in := &v1.AuthzRule{
		Name:          "legacy-wildcard",
		SubjectType:   "*",
		SubjectIds:    []string{"*"},
		ResourceKind:  "agent",
		ResourceNames: []string{"*"},
		Verbs:         []string{"read"},
		Effect:        "allow",
		Status:        "enabled",
	}

	model := AuthzRuleV1ToAuthzRuleModel(in)
	require.Len(t, model.Subjects, 3, "any-subject must become one entry per subject kind")
	kinds := map[string]bool{}
	for _, subject := range model.Subjects {
		kinds[subject.Kind] = true
		assert.Equal(t, "*", subject.Name)
	}
	assert.True(t, kinds["User"])
	assert.True(t, kinds["Group"])
	assert.True(t, kinds["ServiceAccount"])

	out := AuthzRuleModelToAuthzRuleV1(model)
	require.Len(t, out.Subjects, 3)
	// A mixed subject set has no single-kind spelling; the plural list is the
	// complete statement.
	assert.Empty(t, out.SubjectType)
}

func TestAuthzRuleMultiResourceRoundTrip(t *testing.T) {
	in := &v1.AuthzRule{
		Name: "multi",
		Subjects: []*v1.AuthzSubject{
			{Kind: "User", Name: "uuid-alice"},
			{Kind: "Group", Name: "dept-ai"},
		},
		Resources: []*v1.AuthzResource{
			{Kind: "namespace", Names: []string{"helper-public"}},
			{Kind: "agent", Names: []string{"researcher"}},
		},
		Verbs:  []string{"read", "write"},
		Effect: "deny",
		Status: "enabled",
	}

	model := AuthzRuleV1ToAuthzRuleModel(in)
	require.Len(t, model.Subjects, 2)
	require.Len(t, model.Resources, 2)
	assert.Empty(t, model.SubjectType)
	assert.Empty(t, model.ResourceKind)

	out := AuthzRuleModelToAuthzRuleV1(model)
	require.Len(t, out.Subjects, 2)
	require.Len(t, out.Resources, 2)
	assert.Equal(t, "namespace", out.Resources[0].Kind)
	assert.Equal(t, "agent", out.Resources[1].Kind)
	assert.Equal(t, []string{"read", "write"}, out.Verbs)
	assert.Equal(t, "deny", out.Effect)
}
