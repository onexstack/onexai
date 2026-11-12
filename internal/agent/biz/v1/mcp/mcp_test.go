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

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// fakeProber records the row it was asked to dial and answers with fixed data.
type fakeProber struct {
	tools []mcpclient.ProbedTool
	err   error
	got   *model.MCPM
}

func (f *fakeProber) Probe(_ context.Context, server *model.MCPM) ([]mcpclient.ProbedTool, error) {
	f.got = server
	return f.tools, f.err
}

func TestTestReportsTools(t *testing.T) {
	prober := &fakeProber{tools: []mcpclient.ProbedTool{{Name: "k8s_get", Description: "get a resource"}}}
	biz := New(nil, prober)

	resp, err := biz.Test(context.Background(), &v1.TestMCPRequest{Mcp: &v1.MCP{
		Name:     "k8s",
		Protocol: "HTTP",
		Url:      "http://127.0.0.1:9182",
		Status:   model.StatusDisabled,
	}})

	require.NoError(t, err)
	assert.True(t, resp.GetOk())
	assert.GreaterOrEqual(t, resp.GetLatencyMs(), int64(0))
	assert.Equal(t, []*v1.MCPProbedTool{{Name: "k8s_get", Description: "get a resource"}}, resp.GetTools())
	require.NotNil(t, prober.got)
	assert.Equal(t, model.StatusEnabled, prober.got.Status,
		"a connectivity test is about the endpoint, not the enable switch")
}

func TestTestRequiresURL(t *testing.T) {
	prober := &fakeProber{}
	biz := New(nil, prober)

	resp, err := biz.Test(context.Background(), &v1.TestMCPRequest{Mcp: &v1.MCP{Protocol: "HTTP"}})

	require.NoError(t, err)
	assert.False(t, resp.GetOk())
	assert.Equal(t, "url is required", resp.GetMessage())
	assert.Nil(t, prober.got, "an incomplete configuration must not dial anything")
}

func TestTestWithoutProber(t *testing.T) {
	biz := New(nil, nil)

	resp, err := biz.Test(context.Background(), &v1.TestMCPRequest{Mcp: &v1.MCP{
		Protocol: "HTTP",
		Url:      "http://127.0.0.1:9182",
	}})

	require.NoError(t, err)
	assert.False(t, resp.GetOk())
	assert.Equal(t, "MCP runtime is not enabled", resp.GetMessage())
}

func TestTestReturnsProbeError(t *testing.T) {
	prober := &fakeProber{err: errors.New("dial tcp 127.0.0.1:1: connect: connection refused")}
	biz := New(nil, prober)

	resp, err := biz.Test(context.Background(), &v1.TestMCPRequest{Mcp: &v1.MCP{
		Protocol: "HTTP",
		Url:      "http://127.0.0.1:1",
	}})

	require.NoError(t, err, "an unreachable server is a valid response, not an API error")
	assert.False(t, resp.GetOk())
	assert.Equal(t, "dial tcp 127.0.0.1:1: connect: connection refused", resp.GetMessage())
	assert.Empty(t, resp.GetTools())
}
