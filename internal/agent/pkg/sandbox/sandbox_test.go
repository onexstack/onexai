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

package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/model"
)

func TestWorkspacePath(t *testing.T) {
	clean, err := workspacePath("notes.txt")
	require.NoError(t, err)
	assert.Equal(t, "/workspace/notes.txt", clean)

	clean, err = workspacePath("/workspace/a/b.txt")
	require.NoError(t, err)
	assert.Equal(t, "/workspace/a/b.txt", clean)

	clean, err = workspacePath("/tmp/out.log")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/out.log", clean)

	_, err = workspacePath("../../etc/passwd")
	require.Error(t, err)

	_, err = workspacePath("/etc/passwd")
	require.Error(t, err)
}

func TestShellQuote(t *testing.T) {
	assert.Equal(t, `'echo hi'`, shellQuote("echo hi"))
	assert.Equal(t, `'it'\''s'`, shellQuote("it's"))
}

func TestCappedBufferTruncates(t *testing.T) {
	var buffer cappedBuffer
	_, err := buffer.Write(make([]byte, MaxOutputBytes+10))
	require.NoError(t, err)
	assert.True(t, buffer.truncated)
	assert.Contains(t, buffer.String(), "truncated")
}

type fakeStore struct {
	rows map[string]*model.SandboxM
}

func (f *fakeStore) Create(_ context.Context, sandbox *model.SandboxM) error {
	if f.rows == nil {
		f.rows = map[string]*model.SandboxM{}
	}
	f.rows[sandbox.ID] = sandbox
	return nil
}
func (f *fakeStore) Get(_ context.Context, id string) (*model.SandboxM, error) {
	if row, ok := f.rows[id]; ok {
		return row, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (f *fakeStore) Update(context.Context, *model.SandboxM) error { return nil }
func (f *fakeStore) ListExpired(context.Context, int64, int) ([]*model.SandboxM, error) {
	return nil, nil
}
func (f *fakeStore) List(context.Context, string, int) ([]*model.SandboxM, error) {
	return nil, nil
}

func TestGetScopesByOwner(t *testing.T) {
	store := &fakeStore{rows: map[string]*model.SandboxM{
		"id": {ID: "id", Owner: "alice"},
	}}
	service := &Service{Store: store}
	got, err := service.Get(context.Background(), "id", "alice")
	require.NoError(t, err)
	assert.Equal(t, "id", got.ID)

	_, err = service.Get(context.Background(), "id", "bob")
	require.ErrorIs(t, err, ErrNotFound)
}

// TestConfigRefusesAnEscapingNetwork pins the modes that would undo the
// isolation, and confirms the ordinary ones are accepted.
func TestConfigRefusesAnEscapingNetwork(t *testing.T) {
	for _, network := range []string{"host", "container:other", "service:web", "pod:x", "ns:net"} {
		assert.Error(t, Config{Network: network}.Validate(), "network %q", network)
	}
	for _, network := range []string{"", "none", "bridge", "sandbox-egress"} {
		assert.NoError(t, Config{Network: network}.Validate(), "network %q", network)
	}
}

func TestConfigRefusesAnUnparseableProxy(t *testing.T) {
	assert.Error(t, Config{EgressProxy: "://not a url"}.Validate())
	assert.NoError(t, Config{EgressProxy: "http://egress-proxy:3128"}.Validate())
}

// TestRunArgsCarryTheNetworkPolicy pins what the container is actually given:
// the configured network, and the proxy under both spellings, because a tool
// that reads only the lowercase one would otherwise reach the internet directly
// or not at all.
func TestRunArgsCarryTheNetworkPolicy(t *testing.T) {
	service := &Service{Config: Config{
		Network:     "sandbox-egress",
		EgressProxy: "http://egress-proxy:3128",
		NoProxy:     "localhost,127.0.0.1",
	}}
	args := strings.Join(service.runArgs("box", Template{Image: "img"}), " ")

	assert.Contains(t, args, "--network sandbox-egress")
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		assert.Contains(t, args, name+"=http://egress-proxy:3128")
	}
	assert.Contains(t, args, "NO_PROXY=localhost,127.0.0.1")
	assert.Contains(t, args, "no_proxy=localhost,127.0.0.1")
}

// TestRunArgsDefaultToNoNetwork pins the default: a sandbox nobody configured
// has no route out.
func TestRunArgsDefaultToNoNetwork(t *testing.T) {
	args := strings.Join((&Service{}).runArgs("box", Template{Image: "img"}), " ")
	assert.Contains(t, args, "--network none")
	assert.NotContains(t, args, "HTTP_PROXY")
}

// fakeTemplates is a declarative template source.
type fakeTemplates struct {
	template *Template
	err      error
}

func (f fakeTemplates) Template(context.Context, string) (*Template, error) {
	return f.template, f.err
}

// TestTemplateDeclaredWins pins the resolution order: the object an operator
// edits beats the image baked into the deployment's config.
func TestTemplateDeclaredWins(t *testing.T) {
	service := &Service{
		Config:    Config{Images: map[string]string{"python": "from-config:1"}, DefaultImage: "default:1"},
		Templates: fakeTemplates{template: &Template{Image: "from-crd@sha256:abc", TTL: time.Minute}},
	}
	resolved, err := service.templateFor(context.Background(), "python")
	require.NoError(t, err)
	assert.Equal(t, "from-crd@sha256:abc", resolved.Image)
	assert.Equal(t, time.Minute, resolved.ttl())
}

// TestTemplateFallsBackToConfig pins the migration path: a deployment that has
// not adopted templates keeps working unchanged.
func TestTemplateFallsBackToConfig(t *testing.T) {
	service := &Service{
		Config:    Config{Images: map[string]string{"python": "from-config:1"}, DefaultImage: "default:1"},
		Templates: fakeTemplates{},
	}
	resolved, err := service.templateFor(context.Background(), "python")
	require.NoError(t, err)
	assert.Equal(t, "from-config:1", resolved.Image)

	resolved, err = service.templateFor(context.Background(), "unknown")
	require.NoError(t, err)
	assert.Equal(t, "default:1", resolved.Image)
}

func TestTemplateMissingEverywhereIsAnError(t *testing.T) {
	service := &Service{Config: Config{}}
	_, err := service.templateFor(context.Background(), "python")
	require.Error(t, err)
}

// TestTemplateEnvAndCommandReachTheContainer pins what a template actually
// changes about the container.
func TestTemplateEnvAndCommandReachTheContainer(t *testing.T) {
	service := &Service{Config: Config{Command: []string{"sleep", "infinity"}}}
	args := strings.Join(service.runArgs("box", Template{
		Image:   "img@sha256:abc",
		Command: []string{"python", "-m", "http.server"},
		Env:     []string{"LANG=C.UTF-8"},
	}), " ")

	assert.Contains(t, args, "--env LANG=C.UTF-8")
	assert.Contains(t, args, "img@sha256:abc python -m http.server")
	assert.NotContains(t, args, "sleep infinity")
}
