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

package agentcmd

import (
	"context"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/pkg/agentlabel"
)

// AgentInfo is what a rule selects on: the parts of an agent object that reach
// the two selection spaces — its labels and its own columns (fields).
//
// It is a type of this package rather than the API type so the resolver — the
// part with the matching rules in it — can be tested without a control plane,
// and so that a property the matching starts depending on is a change here
// rather than a silent one three packages away.
type AgentInfo struct {
	Name      string
	Namespace string
	// Labels are the agent object's own metadata.labels. They are legal labels
	// by construction — the API server validated them on write — which is why
	// nothing here has to decide what to do with an illegal one.
	Labels map[string]string
}

// AgentLister reads the agents whose labels the rules select on.
type AgentLister interface {
	ListAgentInfo(ctx context.Context) ([]AgentInfo, error)
}

// agentSnapshot is the two selection spaces of every agent the control plane
// holds.
type agentSnapshot struct {
	// names are every agent the plane holds, in the lister's order. They are
	// kept beside the labels because a rule that names agents outright covers
	// exactly these, whether or not each one carries a label.
	names []string
	// set holds each agent's labels. An agent with none has no entry, so a
	// label rule fails closed rather than matching by accident.
	set map[string]labels.Set
	// fields holds each agent's own columns. It is always present — a name and
	// a namespace are strings, not something that can fail to be a label.
	fields map[string]fields.Set
}

// buildAgentSnapshot projects every agent.
func buildAgentSnapshot(infos []AgentInfo) *agentSnapshot {
	snap := &agentSnapshot{
		names:  make([]string, 0, len(infos)),
		set:    make(map[string]labels.Set, len(infos)),
		fields: make(map[string]fields.Set, len(infos)),
	}

	for _, info := range infos {
		if info.Name == "" {
			continue
		}
		snap.names = append(snap.names, info.Name)

		if len(info.Labels) > 0 {
			snap.set[info.Name] = labels.Set(info.Labels)
		}
		snap.fields[info.Name] = agentlabel.Fields(info.Name, info.Namespace)
	}

	return snap
}

// inScope reports whether any of the conversation's agents belongs to the given
// namespace. An agent the snapshot does not know cannot be placed in one, so a
// scoped rule fails closed for it.
func (s *agentSnapshot) inScope(agents []string, namespace string) bool {
	for _, agent := range agents {
		if fs, ok := s.fields[agent]; ok && fs[agentlabel.FieldNamespace] == namespace {
			return true
		}
	}
	return false
}
