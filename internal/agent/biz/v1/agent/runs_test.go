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

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestApprovalIDIsDeterministicAndDistinct(t *testing.T) {
	first := approvalID("run", "pause", "tool-1")
	assert.Equal(t, first, approvalID("run", "pause", "tool-1"))
	assert.NotEqual(t, first, approvalID("run", "pause", "tool-2"))
	assert.NotEqual(t, first, approvalID("run", "pause-2", "tool-1"))
	assert.Contains(t, first, "ap-")
}

func TestFirstAgentName(t *testing.T) {
	assert.Equal(t, "", firstAgentName(nil))
	assert.Equal(t, "a", firstAgentName([]string{"a", "b"}))
}
