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

package di

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	"github.com/onexstack/onexai/internal/agent/pkg/skill"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/controlplanestore"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
	"github.com/onexstack/onexai/pkg/options"
)

// TestHealthProbersLive drives the real adapters — the control-plane store, the
// MCP client and the retrieval platforms — against resources in the control
// plane, so the chain the reporter depends on is exercised end to end: a name in
// a custom resource resolves to the same row a run would read, and the
// subsystem's own answer is what comes back.
func TestHealthProbersLive(t *testing.T) {
	kubeconfig := os.Getenv("ONEXAI_CONTROLPLANE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set ONEXAI_CONTROLPLANE_KUBECONFIG to run against a live oai-declserver")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client, err := declclient.New(kubeconfig, "default", 30*time.Second)
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx, 10*time.Second))

	resources, err := controlplanestore.NewAIStore(client, "default", store.NewEmptyStore(), nil)
	if err != nil {
		t.Fatalf("build the resource store: %v", err)
	}

	// The MCP manager is built the way the service builds it, so the probe goes
	// through the real dialer rather than a fake.
	mcp := mcpclient.NewManager(ctx, resources, mcpclient.Options{Enabled: true})
	skills := skill.NewManager(t.TempDir(), "", skill.ProviderDeps{})

	// A retrieval platform is configured for the second half of the KB test; the
	// base URL points at a local server so the assertion does not depend on a
	// third party.
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/retrieve") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"records":[{"score":0.9,"segment":{"content":"passage","document":{"name":"doc.md"}}}]}`)
	}))
	defer platform.Close()

	kbs := &kbToolSource{dify: options.KnowledgeBaseOptions{BaseURL: platform.URL, APIKey: "probe-key"}}

	probers := ProvideHealthProbers(resources, mcp, skills, kbs)
	require.NotNil(t, probers.MCP)
	require.NotNil(t, probers.Skills)
	require.NotNil(t, probers.KnowledgeBases)

	// ── MCP: an endpoint nothing listens on is a failed handshake ────────────
	servers := client.Clientset.AgentV1alpha1().MCPServers("default")
	dead, err := servers.Create(ctx, &agentv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "probe-dead-mcp", Namespace: "default"},
		Spec: agentv1alpha1.MCPServerSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
			Protocol:   "streamable-http",
			URL:        "http://127.0.0.1:1/mcp",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = servers.Delete(context.Background(), dead.Name, metav1.DeleteOptions{}) })

	// ── Skill: a supported provider whose params cannot be used ──────────────
	// The API server already rejects an unknown provider, so the misconfiguration
	// a probe has to catch is the one that survives validation: a github source
	// with no repository.
	skillClient := client.Clientset.AgentV1alpha1().Skills("default")
	unknown, err := skillClient.Create(ctx, &agentv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "probe-unknown-skill", Namespace: "default"},
		Spec: agentv1alpha1.SkillSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
			Provider:   "github",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = skillClient.Delete(context.Background(), unknown.Name, metav1.DeleteOptions{}) })

	// ── Knowledge bases: one on the configured platform, one on none ─────────
	kbClient := client.Clientset.AgentV1alpha1().KnowledgeBases("default")
	createKB := func(name, platformID string) *agentv1alpha1.KnowledgeBase {
		kb, err := kbClient.Create(ctx, &agentv1alpha1.KnowledgeBase{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: agentv1alpha1.KnowledgeBaseSpec{
				CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
				Platform:   platformID,
				DatasetID:  "ds-probe",
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = kbClient.Delete(context.Background(), name, metav1.DeleteOptions{}) })
		return kb
	}
	answering := createKB("probe-kb-answering", "dify")
	unconfigured := createKB("probe-kb-unconfigured", "ragflow")

	// Every adapter resolves through the informer-backed store, so the objects
	// have to be there before the probes run.
	for _, name := range []string{dead.Name} {
		name := name
		require.Eventually(t, func() bool {
			_, err := resources.MCPs().Get(ctx, where.F("name", name))
			return err == nil
		}, 15*time.Second, 100*time.Millisecond, "mcpserver %q never reached the store", name)
	}
	require.Eventually(t, func() bool {
		_, err := resources.Skills().Get(ctx, where.F("name", unknown.Name))
		return err == nil
	}, 15*time.Second, 100*time.Millisecond, "skill never reached the store")
	for _, kb := range []*agentv1alpha1.KnowledgeBase{answering, unconfigured} {
		kb := kb
		require.Eventually(t, func() bool {
			_, err := resources.KnowledgeBases().Get(ctx, where.F("name", kb.Name))
			return err == nil
		}, 15*time.Second, 100*time.Millisecond, "knowledgebase %q never reached the store", kb.Name)
	}

	// ── the assertions ──────────────────────────────────────────────────────
	_, err = probers.MCP.Probe(ctx, dead.Name)
	require.Error(t, err, "a server on a closed port must not report a handshake")
	assert.Contains(t, err.Error(), "127.0.0.1:1")

	_, err = probers.Skills.Probe(ctx, unknown.Name)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "params is empty")

	passages, err := probers.KnowledgeBases.Probe(ctx, answering.Name)
	require.NoError(t, err, "the configured platform answered, so the probe must succeed")
	assert.Equal(t, 1, passages)

	_, err = probers.KnowledgeBases.Probe(ctx, unconfigured.Name)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no ragflow retrieval platform is configured")
}
