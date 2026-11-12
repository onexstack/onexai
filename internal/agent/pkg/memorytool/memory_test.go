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

package memorytool

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/capability"
)

func TestNoProviderMeansNoTools(t *testing.T) {
	assert.Empty(t, New(nil), "a deployment without memory installs nothing")
}

// TestToolNamesMatchTheGateway pins that a prompt written for a remote runtime
// works unchanged on the in-process one.
func TestToolNamesMatchTheGateway(t *testing.T) {
	assert.Equal(t, "save_memory", saveInfo().Name)
	assert.Equal(t, "load_memory", loadInfo().Name)
	assert.Equal(t, "prefetch_memory", prefetchInfo().Name)
}

// TestRunWithoutAMemoryIdentityIsRefused pins the guard: the identity comes from
// the run, never from the model's arguments, so a run that carries none cannot
// read or write anyone's memory.
func TestRunWithoutAMemoryIdentityIsRefused(t *testing.T) {
	invokable := &memoryTool{
		provider: &capability.MemoryProvider{},
		name:     SaveToolName,
		info:     saveInfo(),
	}
	_, err := invokable.InvokableRun(context.Background(), `{"content":"x"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no memory identity")
}
