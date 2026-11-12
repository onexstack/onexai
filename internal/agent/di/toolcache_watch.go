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
	"log/slog"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/tools/cache"

	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// registerToolCacheInvalidators wires the control-plane informers to the MCP
// tool cache.
//
// The cache is keyed by (servers, agents), but what an agent actually sees also
// depends on its resolved capabilities and on the CustomCommand catalog. Those
// change outside this process — the controllers write status, an operator edits
// a command — so the cache cannot learn about them from its own read path. The
// informers are that signal.
//
// An Agent update is narrowed to a change in status.resolved: that is the
// controller's answer to "what does this agent have", and it is exactly what
// the per-caller tool filter reads. Prompt aggregation, A2A cards and condition
// churn must not throw the tool cache away.
func registerToolCacheInvalidators(client *declclient.Client, inv mcpclient.Invalidator) {
	if client == nil || inv == nil {
		return
	}

	agentInformer := client.Factory.Agent().V1alpha1().Agents().Informer()
	_, err := agentInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if agent, ok := obj.(*agentv1alpha1.Agent); ok {
				inv.InvalidateAgent(agent.Name)
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			oldAgent, okOld := oldObj.(*agentv1alpha1.Agent)
			newAgent, okNew := newObj.(*agentv1alpha1.Agent)
			if !okOld || !okNew {
				return
			}
			if !agentResolvedChanged(oldAgent, newAgent) {
				return
			}
			inv.InvalidateAgent(newAgent.Name)
		},
		DeleteFunc: func(obj any) {
			if agent, ok := obj.(*agentv1alpha1.Agent); ok {
				inv.InvalidateAgent(agent.Name)
			}
		},
	})
	if err != nil {
		slog.Warn("cannot watch agents for MCP tool cache invalidation", "error", err)
	}

	// These resources change the tool set for every agent that references them,
	// and their writes are infrequent enough that a blanket invalidation is
	// cheaper than working out which agents were affected.
	for name, informer := range map[string]cache.SharedIndexInformer{
		"agentbindings":        client.Factory.Agent().V1alpha1().AgentBindings().Informer(),
		"clusteragentbindings": client.Factory.Agent().V1alpha1().ClusterAgentBindings().Informer(),
		"customcommands":       client.Factory.Agent().V1alpha1().CustomCommands().Informer(),
		"mcpservers":           client.Factory.Agent().V1alpha1().MCPServers().Informer(),
		"agenttemplates":       client.Factory.Agent().V1alpha1().AgentTemplates().Informer(),
	} {
		_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(any) { inv.Invalidate() },
			UpdateFunc: func(any, any) { inv.Invalidate() },
			DeleteFunc: func(any) { inv.Invalidate() },
		})
		if err != nil {
			slog.Warn("cannot watch resource for MCP tool cache invalidation", "informer", name, "error", err)
		}
	}
}

// agentResolvedChanged reports whether the controller's answer to "what does
// this agent have" changed. Only status.resolved decides which tools the agent
// sees; prompt aggregation, cards and condition churn must not cost a
// re-discovery.
func agentResolvedChanged(oldAgent, newAgent *agentv1alpha1.Agent) bool {
	return !equality.Semantic.DeepEqual(oldAgent.Status.Resolved, newAgent.Status.Resolved)
}
