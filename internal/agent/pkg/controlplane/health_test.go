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
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func mcpServerFixture() *agentv1alpha1.MCPServer {
	return &agentv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "kpanda", Namespace: "default", Generation: 3},
		Spec: agentv1alpha1.MCPServerSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
			Protocol:   "streamable-http",
			URL:        "http://kpanda:8091/mcp",
		},
	}
}

func TestMCPServerStatus_PublishesWhatTheProbeFound(t *testing.T) {
	status := mcpServerStatus(mcpServerFixture(), []ProbeTool{
		{Name: "list_clusters", Description: "list the fleet"},
		{Name: "get_cluster"},
	}, nil)

	if len(status.Conditions) != 1 {
		t.Fatalf("the probe published %d conditions, want 1 (HandshakeReady)", len(status.Conditions))
	}
	handshake := status.Conditions[0]
	if handshake.Type == nil || *handshake.Type != agentv1alpha1.ConditionHandshakeReady {
		t.Fatalf("condition type = %v, want %s", handshake.Type, agentv1alpha1.ConditionHandshakeReady)
	}
	if handshake.Status == nil || *handshake.Status != metav1.ConditionTrue {
		t.Errorf("status = %v, want True", handshake.Status)
	}
	if handshake.Reason == nil || *handshake.Reason != "HandshakeSucceeded" {
		t.Errorf("reason = %v, want HandshakeSucceeded", handshake.Reason)
	}
	if handshake.ObservedGeneration == nil || *handshake.ObservedGeneration != 3 {
		t.Errorf("observedGeneration = %v, want 3", handshake.ObservedGeneration)
	}

	if len(status.Tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(status.Tools))
	}
	if status.Tools[0].Name == nil || *status.Tools[0].Name != "list_clusters" {
		t.Errorf("first tool = %v, want list_clusters", status.Tools[0].Name)
	}
	if status.Tools[0].Description == nil || *status.Tools[0].Description != "list the fleet" {
		t.Errorf("first tool's description = %v", status.Tools[0].Description)
	}
	if status.Tools[1].Description != nil {
		t.Error("a tool without a description must not be given one")
	}
	if status.LastDiscoverTime == nil {
		t.Error("a successful discovery must stamp lastDiscoverTime")
	}
}

// TestMCPServerStatus_KeepsTheLastGoodDiscovery pins that a failed probe reports
// the failure without erasing what the server last published: status.tools is
// "what the last successful discovery found", and clearing it would make a
// transient network fault look like a server with no tools.
func TestMCPServerStatus_KeepsTheLastGoodDiscovery(t *testing.T) {
	server := mcpServerFixture()
	previous := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	server.Status.Tools = []agentv1alpha1.MCPTool{{Name: "list_clusters"}}
	server.Status.LastDiscoverTime = &previous

	status := mcpServerStatus(server, nil, errors.New("dial tcp: connection refused"))

	handshake := status.Conditions[0]
	if handshake.Status == nil || *handshake.Status != metav1.ConditionFalse {
		t.Errorf("status = %v, want False", handshake.Status)
	}
	if handshake.Reason == nil || *handshake.Reason != "HandshakeFailed" {
		t.Errorf("reason = %v, want HandshakeFailed", handshake.Reason)
	}
	if handshake.Message == nil || !strings.Contains(*handshake.Message, "connection refused") {
		t.Errorf("message = %v, want the dial error in it", handshake.Message)
	}

	if len(status.Tools) != 1 || status.Tools[0].Name == nil || *status.Tools[0].Name != "list_clusters" {
		t.Errorf("the failed probe rewrote the tool list: %v", status.Tools)
	}
	if status.LastDiscoverTime == nil || !status.LastDiscoverTime.Equal(&previous) {
		t.Errorf("lastDiscoverTime = %v, want the previous %v", status.LastDiscoverTime, previous)
	}
}

// TestCondition_KeepsTheTransitionTimeWhenTheStatusIsUnchanged pins the one rule
// every prober shares: a steady state must not rewrite lastTransitionTime, or
// every probe interval would look like a status change.
func TestCondition_KeepsTheTransitionTimeWhenTheStatusIsUnchanged(t *testing.T) {
	original := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	existing := []metav1.Condition{{
		Type:               agentv1alpha1.ConditionReachable,
		Status:             metav1.ConditionTrue,
		Reason:             "RetrievalSucceeded",
		LastTransitionTime: original,
	}}

	kept := condition(existing, 1, agentv1alpha1.ConditionReachable, true, "RetrievalSucceeded", "again")
	if !kept.LastTransitionTime.Equal(&original) {
		t.Errorf("lastTransitionTime moved on an unchanged status: %v", kept.LastTransitionTime)
	}

	moved := condition(existing, 1, agentv1alpha1.ConditionReachable, false, "RetrievalFailed", "down")
	if moved.LastTransitionTime.Equal(&original) {
		t.Error("a status change must stamp a new transition time")
	}
}

// TestProbersAreOptional pins the constructor's contract: a deployment without a
// subsystem passes nil, and the reporter then skips that resource kind instead of
// inventing a failure for it.
func TestProbersAreOptional(t *testing.T) {
	reporter := NewHealthReporter(nil, time.Minute, Probers{}, "")
	if reporter.probers.MCP != nil || reporter.probers.Skills != nil || reporter.probers.KnowledgeBases != nil {
		t.Error("a reporter built without probers gained some")
	}
}
