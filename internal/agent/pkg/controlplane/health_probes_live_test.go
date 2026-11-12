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

package controlplane

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// The fake probers below stand in for the MCP client, the skill sources and the
// retrieval platforms. What is under test here is the publishing half — which
// condition each probe writes, and what a failure leaves alone — so the external
// systems are deliberately not involved: a test that needed a live MCP server and
// a live RAGFlow would fail for reasons that say nothing about this code.

type fakeMCPProber struct {
	tools []ProbeTool
	err   error
}

func (p *fakeMCPProber) Probe(context.Context, string) ([]ProbeTool, error) { return p.tools, p.err }

type fakeSkillProber struct {
	count int
	err   error
}

func (p *fakeSkillProber) Probe(context.Context, string) (int, error) { return p.count, p.err }

type fakeKBProber struct {
	passages int
	err      error
}

func (p *fakeKBProber) Probe(context.Context, string) (int, error) { return p.passages, p.err }

func TestRuntimeProbesLive(t *testing.T) {
	kubeconfig := os.Getenv("ONEXAI_CONTROLPLANE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set ONEXAI_CONTROLPLANE_KUBECONFIG to run against a live oai-declserver")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client, err := declclient.New(kubeconfig, "default", 30*time.Second)
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx, 10*time.Second))

	// ── create the three resources ──────────────────────────────────────────
	servers := client.Clientset.AgentV1alpha1().MCPServers("default")
	server, err := servers.Create(ctx, &agentv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "probe-live-mcp", Namespace: "default"},
		Spec: agentv1alpha1.MCPServerSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
			Protocol:   "streamable-http",
			URL:        "http://127.0.0.1:1/mcp",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = servers.Delete(context.Background(), server.Name, metav1.DeleteOptions{}) })

	skillClient := client.Clientset.AgentV1alpha1().Skills("default")
	skill, err := skillClient.Create(ctx, &agentv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "probe-live-skill", Namespace: "default"},
		Spec: agentv1alpha1.SkillSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
			Provider:   "github",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = skillClient.Delete(context.Background(), skill.Name, metav1.DeleteOptions{}) })

	kbClient := client.Clientset.AgentV1alpha1().KnowledgeBases("default")
	kb, err := kbClient.Create(ctx, &agentv1alpha1.KnowledgeBase{
		ObjectMeta: metav1.ObjectMeta{Name: "probe-live-kb", Namespace: "default"},
		Spec: agentv1alpha1.KnowledgeBaseSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
			Platform:   "ragflow",
			DatasetID:  "probe-live-dataset",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = kbClient.Delete(context.Background(), kb.Name, metav1.DeleteOptions{}) })

	// Every prober reads the informer cache, so the objects have to be there
	// before the first pass or it silently skips them.
	for _, check := range []struct {
		what string
		seen func() bool
	}{
		{"mcpserver", func() bool { _, err := client.MCPServers().MCPServers("default").Get(server.Name); return err == nil }},
		{"skill", func() bool { _, err := client.Skills().Skills("default").Get(skill.Name); return err == nil }},
		{"knowledgebase", func() bool {
			_, err := client.KnowledgeBases().KnowledgeBases("default").Get(kb.Name)
			return err == nil
		}},
	} {
		require.Eventually(t, check.seen, 15*time.Second, 100*time.Millisecond,
			"%s never reached the informer cache", check.what)
	}

	// ── the happy path: each probe publishes what it found ──────────────────
	mcpProber := &fakeMCPProber{tools: []ProbeTool{{Name: "list_clusters", Description: "the fleet"}}}
	skillProber := &fakeSkillProber{count: 3}
	kbProber := &fakeKBProber{passages: 1}
	reporter := NewHealthReporter(client, time.Minute, Probers{
		MCP: mcpProber, Skills: skillProber, KnowledgeBases: kbProber,
	}, "")

	reporter.probeMCPServers(ctx)
	reporter.probeSkills(ctx)
	reporter.probeKnowledgeBases(ctx)

	require.Eventually(t, func() bool {
		latest, err := servers.Get(ctx, server.Name, metav1.GetOptions{})
		return err == nil && apimeta.FindStatusCondition(latest.Status.Conditions, agentv1alpha1.ConditionHandshakeReady) != nil
	}, 15*time.Second, 100*time.Millisecond, "the MCP handshake was never reported")

	latestServer, err := servers.Get(ctx, server.Name, metav1.GetOptions{})
	require.NoError(t, err)
	handshake := apimeta.FindStatusCondition(latestServer.Status.Conditions, agentv1alpha1.ConditionHandshakeReady)
	require.NotNil(t, handshake)
	assert.Equal(t, metav1.ConditionTrue, handshake.Status)
	assert.Equal(t, "HandshakeSucceeded", handshake.Reason)
	require.Len(t, latestServer.Status.Tools, 1)
	assert.Equal(t, "list_clusters", latestServer.Status.Tools[0].Name)
	assert.NotNil(t, latestServer.Status.LastDiscoverTime)

	latestSkill, err := skillClient.Get(ctx, skill.Name, metav1.GetOptions{})
	require.NoError(t, err)
	reachable := apimeta.FindStatusCondition(latestSkill.Status.Conditions, agentv1alpha1.ConditionSourceReachable)
	require.NotNil(t, reachable, "the skill source was never reported")
	assert.Equal(t, metav1.ConditionTrue, reachable.Status)
	assert.Contains(t, reachable.Message, "3 skills")
	require.NotNil(t, latestSkill.Status.LastSyncTime)

	latestKB, err := kbClient.Get(ctx, kb.Name, metav1.GetOptions{})
	require.NoError(t, err)
	kbReachable := apimeta.FindStatusCondition(latestKB.Status.Conditions, agentv1alpha1.ConditionReachable)
	require.NotNil(t, kbReachable, "the knowledge base was never reported")
	assert.Equal(t, metav1.ConditionTrue, kbReachable.Status)
	require.NotNil(t, latestKB.Status.LastCheckTime)
	confirmedAt := *latestKB.Status.LastCheckTime

	// ── the failing path: the condition flips, the evidence stays ───────────
	mcpProber.tools, mcpProber.err = nil, errors.New("dial tcp: connection refused")
	skillProber.count, skillProber.err = 0, errors.New("the source answered 404")
	kbProber.passages, kbProber.err = 0, errors.New("the platform answered 401")

	reporter.probeMCPServers(ctx)
	reporter.probeSkills(ctx)
	reporter.probeKnowledgeBases(ctx)

	require.Eventually(t, func() bool {
		latest, err := servers.Get(ctx, server.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		condition := apimeta.FindStatusCondition(latest.Status.Conditions, agentv1alpha1.ConditionHandshakeReady)
		return condition != nil && condition.Status == metav1.ConditionFalse
	}, 15*time.Second, 100*time.Millisecond, "the refused handshake was never reported")

	latestServer, err = servers.Get(ctx, server.Name, metav1.GetOptions{})
	require.NoError(t, err)
	handshake = apimeta.FindStatusCondition(latestServer.Status.Conditions, agentv1alpha1.ConditionHandshakeReady)
	assert.Equal(t, "HandshakeFailed", handshake.Reason)
	assert.Contains(t, handshake.Message, "connection refused")
	assert.Len(t, latestServer.Status.Tools, 1, "a failed probe must not erase the last discovery")

	latestSkill, err = skillClient.Get(ctx, skill.Name, metav1.GetOptions{})
	require.NoError(t, err)
	reachable = apimeta.FindStatusCondition(latestSkill.Status.Conditions, agentv1alpha1.ConditionSourceReachable)
	assert.Equal(t, metav1.ConditionFalse, reachable.Status)
	assert.Contains(t, reachable.Message, "404")

	latestKB, err = kbClient.Get(ctx, kb.Name, metav1.GetOptions{})
	require.NoError(t, err)
	kbReachable = apimeta.FindStatusCondition(latestKB.Status.Conditions, agentv1alpha1.ConditionReachable)
	assert.Equal(t, metav1.ConditionFalse, kbReachable.Status)
	assert.Contains(t, kbReachable.Message, "401")
	require.NotNil(t, latestKB.Status.LastCheckTime)
	assert.True(t, confirmedAt.Equal(latestKB.Status.LastCheckTime),
		"a failed probe must not claim a fresh confirmation")
}
