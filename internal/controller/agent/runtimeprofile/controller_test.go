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

package runtimeprofile

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func TestResolveInProcess(t *testing.T) {
	r := &Reconciler{}
	endpoint, ready, reason, _ := r.resolve(context.Background(), &agentv1alpha1.RuntimeProfile{
		Spec: agentv1alpha1.RuntimeProfileSpec{Kind: agentv1alpha1.RuntimeKindInproc},
	})
	assert.Empty(t, endpoint)
	assert.True(t, ready)
	assert.Equal(t, "InProcess", reason)
}

func TestResolveStaticEndpoint(t *testing.T) {
	r := &Reconciler{}
	endpoint, ready, reason, _ := r.resolve(context.Background(), &agentv1alpha1.RuntimeProfile{
		Spec: agentv1alpha1.RuntimeProfileSpec{
			Kind:     agentv1alpha1.RuntimeKindA2A,
			Endpoint: "https://kagent.example.com",
		},
	})
	assert.Equal(t, "https://kagent.example.com", endpoint)
	assert.True(t, ready)
	assert.Equal(t, "EndpointDeclared", reason)
}

func TestResolveRejectsABadEndpoint(t *testing.T) {
	r := &Reconciler{}
	_, ready, reason, _ := r.resolve(context.Background(), &agentv1alpha1.RuntimeProfile{
		Spec: agentv1alpha1.RuntimeProfileSpec{
			Kind:     agentv1alpha1.RuntimeKindA2A,
			Endpoint: "ftp://kagent",
		},
	})
	assert.False(t, ready)
	assert.Equal(t, "InvalidEndpoint", reason)
}

func TestCapabilitiesFollowKind(t *testing.T) {
	inproc := capabilitiesOf(&agentv1alpha1.RuntimeProfile{
		Spec: agentv1alpha1.RuntimeProfileSpec{Kind: agentv1alpha1.RuntimeKindInproc},
	})
	assert.Equal(t, agentv1alpha1.HistoryModeGateway, inproc.HistoryMode)
	assert.Equal(t, agentv1alpha1.ResumeModeCheckpoint, inproc.ResumeMode)

	remote := capabilitiesOf(&agentv1alpha1.RuntimeProfile{
		Spec: agentv1alpha1.RuntimeProfileSpec{Kind: agentv1alpha1.RuntimeKindA2A},
	})
	assert.Equal(t, agentv1alpha1.HistoryModeRuntime, remote.HistoryMode)
	assert.Equal(t, agentv1alpha1.ResumeModeTask, remote.ResumeMode)
}

func TestValidateEndpoint(t *testing.T) {
	require.NoError(t, validateEndpoint("http://127.0.0.1:8080"))
	require.Error(t, validateEndpoint("kagent:8080"))
	require.Error(t, validateEndpoint("ftp://kagent"))
}
