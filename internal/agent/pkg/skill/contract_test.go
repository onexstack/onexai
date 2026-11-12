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

package skill

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
)

func TestContractBackendServesContract(t *testing.T) {
	backend, err := newContractBackend(&model.SkillM{
		Name:        "k8s-debug",
		Description: "Kubernetes troubleshooting",
		Mode:        "guide",
		Contract:    "when_to_use: pod is CrashLoopBackOff",
	})
	require.NoError(t, err)

	listed, err := backend.List(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "k8s-debug", listed[0].Name)
	assert.Equal(t, "Kubernetes troubleshooting", listed[0].Description)

	loaded, err := backend.Get(context.Background(), "k8s-debug")
	require.NoError(t, err)
	assert.Equal(t, "when_to_use: pod is CrashLoopBackOff", loaded.Content)

	_, err = backend.Get(context.Background(), "other")
	require.Error(t, err)
}

func TestContractBackendRequiresContract(t *testing.T) {
	_, err := newContractBackend(&model.SkillM{Name: "k8s-debug", Mode: "guide"})
	require.Error(t, err)
}

// TestBuildGuideBackendWithoutProvider proves a guide skill does not need a
// provider (or its params) to be usable. Before this, a guide record with an
// empty params field was reported as an unreachable source and its contract
// could never be loaded.
func TestBuildGuideBackendWithoutProvider(t *testing.T) {
	backend, provider, err := buildBackendE(context.Background(), &model.SkillM{
		Name:     "k8s-debug",
		Mode:     "guide",
		Provider: "github",
		Contract: "when_to_use: pod is CrashLoopBackOff",
	}, BuildSpec{})
	require.NoError(t, err)
	assert.Equal(t, "guide", provider)
	require.NotNil(t, backend)

	loaded, err := backend.Get(context.Background(), "k8s-debug")
	require.NoError(t, err)
	assert.Equal(t, "when_to_use: pod is CrashLoopBackOff", loaded.Content)
}
