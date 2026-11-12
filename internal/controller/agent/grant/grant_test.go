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

package grant

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

func TestAllows(t *testing.T) {
	index := sharing.NewGrantIndex([]sharing.Grant{{
		Namespace:     "default",
		FromGroup:     sharing.GroupName,
		FromKind:      AgentKind,
		FromNamespace: "helper-public",
		ToGroup:       sharing.GroupName,
		ToKind:        "Skill",
		ToName:        "k8s-debug",
	}})

	assert.True(t, Allows(index, "helper-public", "default", "Skill", "k8s-debug"))
	assert.False(t, Allows(index, "helper-public", "default", "Skill", "other"))
	assert.True(t, Allows(index, "helper-public", "helper-public", "Skill", "local"),
		"same namespace needs no grant")
	assert.False(t, Allows(nil, "helper-public", "default", "Skill", "k8s-debug"))
	assert.True(t, Allows(nil, "helper-public", "helper-public", "Skill", "local"))
}
