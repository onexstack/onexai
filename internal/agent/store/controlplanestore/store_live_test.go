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

package controlplanestore

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/pkg/declclient"
)

// TestStoreLive exercises the control-plane store end to end: write through the
// API server, read it back through the informer, and check that the row shape
// the runtime consumes survives the round trip.
func TestStoreLive(t *testing.T) {
	kubeconfig := os.Getenv("ONEXAI_CONTROLPLANE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set ONEXAI_CONTROLPLANE_KUBECONFIG to run against a live oai-declserver")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := declclient.New(kubeconfig, "default", 30*time.Second)
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx, 10*time.Second))

	agents := NewAgentStore(client, "default")
	prompts := NewPromptStore(client, "default")

	created := &model.AgentM{
		Name:            "store-live-agent",
		DisplayName:     "Store live agent",
		Description:     "written through the store",
		Status:          "",
		Model:           "deepseek-chat",
		PromptRefs:      model.StringList{"store-live-prompt"},
		Skills:          model.StringList{"k8s-debug"},
		MCPs:            model.StringList{"observability"},
		KnowledgeBases:  model.StringList{"kb-k8s"},
		RequireApproval: model.StringList{"kubectl_*"},
		Webhooks:        model.StringList{"audit-hook"},
		AuthzBackends:   `["opa"]`,
		Namespace:       "default",
		Labels:          model.StringMap{"sre": "", "tier": "prod"},
	}
	require.NoError(t, agents.Create(ctx, created))
	t.Cleanup(func() { _ = agents.Delete(context.Background(), where.F("name", created.Name)) })

	// The write went through the API server; the read comes from the informer,
	// which may lag by one event.
	var row *model.AgentM
	require.Eventually(t, func() bool {
		row, err = agents.Get(ctx, where.F("name", created.Name))
		return err == nil && row != nil
	}, 15*time.Second, 100*time.Millisecond, "the created agent never reached the lister")
	require.NotNil(t, row)

	assert.Equal(t, "Store live agent", row.DisplayName)
	assert.Equal(t, "deepseek-chat", row.Model)
	assert.Equal(t, model.StringList{"store-live-prompt"}, row.PromptRefs)
	assert.ElementsMatch(t, []string{"k8s-debug"}, []string(row.Skills))
	assert.ElementsMatch(t, []string{"observability"}, []string(row.MCPs))
	assert.ElementsMatch(t, []string{"kb-k8s"}, []string(row.KnowledgeBases))
	assert.ElementsMatch(t, []string{"kubectl_*"}, []string(row.RequireApproval))
	assert.Equal(t, model.StringMap{"sre": "", "tier": "prod"}, row.Labels)
	assert.Equal(t, "default", row.Namespace)
	assert.True(t, row.Enabled())

	// A write through the store must be visible to the next read; disabling is
	// how a resource is withdrawn without losing its history. The Agent
	// controller patches status on its own schedule, so the update re-reads and
	// retries instead of writing a stale resourceVersion back.
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := agents.Get(ctx, where.F("name", created.Name))
		if err != nil {
			return err
		}
		if latest == nil {
			return fmt.Errorf("agent %q disappeared", created.Name)
		}
		latest.Status = model.StatusDisabled
		return agents.Update(ctx, latest)
	}))

	require.Eventually(t, func() bool {
		latest, err := agents.Get(ctx, where.F("name", created.Name))
		return err == nil && latest != nil && !latest.Enabled()
	}, 15*time.Second, 100*time.Millisecond, "the update never reached the lister")

	// Prompts round-trip through the same path, with content <-> template.
	prompt := &model.PromptM{Name: "store-live-prompt", Content: "You are an SRE assistant.", Status: ""}
	require.NoError(t, prompts.Create(ctx, prompt))
	t.Cleanup(func() { _ = prompts.Delete(context.Background(), where.F("name", prompt.Name)) })

	require.Eventually(t, func() bool {
		got, err := prompts.Get(ctx, where.F("name", prompt.Name))
		return err == nil && got != nil && got.Content == "You are an SRE assistant."
	}, 15*time.Second, 100*time.Millisecond, "the prompt never round-tripped")

	// Webhooks round-trip through the same path, including the fields that only
	// exist on the object (headers, template, failure mode).
	webhooks := NewWebhookStore(client, "default")
	hook := &model.WebhookM{
		Name:           "store-live-hook",
		URL:            "https://hook.example.internal/end",
		Events:         model.StringList{"onEnd"},
		TimeoutSeconds: 5,
		FailureMode:    "fail",
		Headers:        model.StringMap{"x-tenant": "t1"},
		Template:       `{"hello":"world"}`,
		SecretRef:      "sec-1",
	}
	require.NoError(t, webhooks.Create(ctx, hook))
	t.Cleanup(func() { _ = webhooks.Delete(context.Background(), where.F("name", hook.Name)) })

	require.Eventually(t, func() bool {
		got, err := webhooks.Get(ctx, where.F("name", hook.Name))
		if err != nil || got == nil {
			return false
		}
		return got.FailureMode == "fail" && got.Headers["x-tenant"] == "t1"
	}, 15*time.Second, 100*time.Millisecond, "the webhook never round-tripped")

	gotHook, err := webhooks.Get(ctx, where.F("name", hook.Name))
	require.NoError(t, err)
	require.NotNil(t, gotHook)
	assert.Equal(t, "https://hook.example.internal/end", gotHook.URL)
	assert.Equal(t, "fail", gotHook.FailureMode)
	assert.Equal(t, "t1", gotHook.Headers["x-tenant"])
	assert.Equal(t, `{"hello":"world"}`, gotHook.Template)
	assert.Equal(t, "sec-1", gotHook.SecretRef)
	assert.ElementsMatch(t, []string{"onEnd"}, []string(gotHook.Events))

	// A model's API key lives in a Secret the store owns; the row shape still
	// carries it, because that is what the runtime reads.
	models := NewModelStore(client, "default")
	modelRow := &model.ModelM{
		Name:     "store-live-model",
		Provider: "deepseek",
		BaseURL:  "https://api.deepseek.com",
		Model:    "deepseek-chat",
		APIKey:   "sk-store-live",
		Status:   "",
	}
	require.NoError(t, models.Create(ctx, modelRow))
	t.Cleanup(func() { _ = models.Delete(context.Background(), where.F("name", modelRow.Name)) })

	require.Eventually(t, func() bool {
		got, err := models.Get(ctx, where.F("name", modelRow.Name))
		return err == nil && got != nil && got.APIKey == "sk-store-live"
	}, 20*time.Second, 100*time.Millisecond, "the model's API key never round-tripped through its Secret")

	// Headers of an MCP server are a Secret the store owns, surfaced again as the
	// JSON column the MCP client parses.
	mcps := NewMCPStore(client, "default")
	mcpRow := &model.MCPM{
		Name:     "store-live-mcp",
		Protocol: "streamable-http",
		URL:      "https://mcp.example.internal/sse",
		Headers:  `{"x-tenant":"t1"}`,
		Status:   "",
	}
	require.NoError(t, mcps.Create(ctx, mcpRow))
	t.Cleanup(func() { _ = mcps.Delete(context.Background(), where.F("name", mcpRow.Name)) })

	require.Eventually(t, func() bool {
		got, err := mcps.Get(ctx, where.F("name", mcpRow.Name))
		return err == nil && got != nil && got.Headers == `{"x-tenant":"t1"}`
	}, 20*time.Second, 100*time.Millisecond, "the MCP headers never round-tripped through their Secret")

	ownerRefs := func() int {
		secret, err := client.KubeClient.CoreV1().Secrets("default").Get(ctx, "store-live-mcp-headers", metav1.GetOptions{})
		if err != nil {
			return 0
		}
		return len(secret.OwnerReferences)
	}
	assert.Equal(t, 1, ownerRefs(), "the headers Secret must be owned by the MCPServer so deleting it reclaims the Secret")
}

// TestPromptRefsLive covers the ordered binding list end to end: the agent row
// round-trips spec.prompts in order, and a row with no opinion leaves the
// binding alone.
func TestPromptRefsLive(t *testing.T) {
	kubeconfig := os.Getenv("ONEXAI_CONTROLPLANE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set ONEXAI_CONTROLPLANE_KUBECONFIG to run against a live oai-declserver")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := declclient.New(kubeconfig, "default", 30*time.Second)
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx, 10*time.Second))

	agents := NewAgentStore(client, "default")
	prompts := NewPromptStore(client, "default")
	name := "store-live-prompts"

	t.Cleanup(func() {
		_ = agents.Delete(context.Background(), where.F("name", name))
		for _, prompt := range []string{"store-live-p1", "store-live-p2"} {
			_ = prompts.Delete(context.Background(), where.F("name", prompt))
		}
	})
	for _, prompt := range []string{"store-live-p1", "store-live-p2"} {
		require.NoError(t, prompts.Create(ctx, &model.PromptM{Name: prompt, Content: "prompt " + prompt}))
	}

	created := &model.AgentM{
		Name:        name,
		DisplayName: "Store live prompts agent",
		Status:      "",
		PromptRefs:  model.StringList{"store-live-p1", "store-live-p2"},
		Namespace:   "default",
	}
	require.NoError(t, agents.Create(ctx, created))

	require.Eventually(t, func() bool {
		row, err := agents.Get(ctx, where.F("name", name))
		return err == nil && row != nil && slices.Equal([]string(row.PromptRefs), []string{"store-live-p1", "store-live-p2"})
	}, 15*time.Second, 100*time.Millisecond, "the prompt list did not round-trip in order")

	// Reversing the list is a real change and must be written.
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := agents.Get(ctx, where.F("name", name))
		if err != nil {
			return err
		}
		if latest == nil {
			return fmt.Errorf("agent %q disappeared", name)
		}
		latest.PromptRefs = model.StringList{"store-live-p2", "store-live-p1"}
		return agents.Update(ctx, latest)
	}))
	require.Eventually(t, func() bool {
		row, err := agents.Get(ctx, where.F("name", name))
		return err == nil && row != nil && slices.Equal([]string(row.PromptRefs), []string{"store-live-p2", "store-live-p1"})
	}, 15*time.Second, 100*time.Millisecond, "the reversed prompt list was not written")

	// A row with no prompt opinion must not clear the binding.
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := agents.Get(ctx, where.F("name", name))
		if err != nil {
			return err
		}
		if latest == nil {
			return fmt.Errorf("agent %q disappeared", name)
		}
		latest.PromptRefs = nil
		latest.Description = "no prompt opinion"
		return agents.Update(ctx, latest)
	}))
	require.Eventually(t, func() bool {
		row, err := agents.Get(ctx, where.F("name", name))
		return err == nil && row != nil && row.Description == "no prompt opinion"
	}, 15*time.Second, 100*time.Millisecond, "the update never reached the lister")

	row, err := agents.Get(ctx, where.F("name", name))
	require.NoError(t, err)
	assert.Equal(t, model.StringList{"store-live-p2", "store-live-p1"}, row.PromptRefs,
		"a nil list means no opinion, not clear the binding")
}
