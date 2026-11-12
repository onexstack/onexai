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
	"strings"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	"github.com/onexstack/onexai/internal/agent/pkg/skill"
	"github.com/onexstack/onexai/internal/agent/store"
)

// noRows is a resource store that holds nothing. Its Get answers (nil, nil) —
// the reader's contract for an absent name, which is also what a resource in
// another namespace looks like to a namespace-scoped runtime.
type noRows struct{ store.AIStore }

func (noRows) MCPs() store.MCPStore                     { return noRowsMCP{} }
func (noRows) Skills() store.SkillStore                 { return noRowsSkill{} }
func (noRows) KnowledgeBases() store.KnowledgeBaseStore { return noRowsKB{} }

type noRowsMCP struct{ store.MCPStore }

func (noRowsMCP) Get(context.Context, *where.Options) (*model.MCPM, error) { return nil, nil }

type noRowsSkill struct{ store.SkillStore }

func (noRowsSkill) Get(context.Context, *where.Options) (*model.SkillM, error) { return nil, nil }

type noRowsKB struct{ store.KnowledgeBaseStore }

func (noRowsKB) Get(context.Context, *where.Options) (*model.KnowledgeBaseM, error) { return nil, nil }

// TestHealthProbers_AnAbsentResourceIsReported pins the trap every adapter has
// to handle: the store answers (nil, nil) for a name it does not hold, so an
// unchecked result reaches the subsystem as a nil pointer and takes the process
// down instead of reporting a condition.
func TestHealthProbers_AnAbsentResourceIsReported(t *testing.T) {
	resources := noRows{}
	probers := ProvideHealthProbers(
		resources,
		mcpclient.NewManager(context.Background(), resources, mcpclient.Options{Enabled: true}),
		skill.NewManager(t.TempDir(), "", skill.ProviderDeps{}),
		&kbToolSource{},
	)

	if _, err := probers.MCP.Probe(context.Background(), "absent"); err == nil ||
		!strings.Contains(err.Error(), "not in the runtime's resource store") {
		t.Errorf("MCP probe error = %v, want an absent-resource error", err)
	}
	if _, err := probers.Skills.Probe(context.Background(), "absent"); err == nil ||
		!strings.Contains(err.Error(), "not in the runtime's resource store") {
		t.Errorf("skill probe error = %v, want an absent-resource error", err)
	}
	if _, err := probers.KnowledgeBases.Probe(context.Background(), "absent"); err == nil ||
		!strings.Contains(err.Error(), "not in the runtime's resource store") {
		t.Errorf("knowledge base probe error = %v, want an absent-resource error", err)
	}
}

// TestProvideHealthProbers_OmitsAnUnconfiguredSubsystem pins that a deployment
// with, say, MCP switched off gets no MCP prober — the reporter then leaves those
// resources' conditions alone instead of publishing a failure it invented.
func TestProvideHealthProbers_OmitsAnUnconfiguredSubsystem(t *testing.T) {
	probers := ProvideHealthProbers(noRows{}, nil, nil, nil)
	if probers.MCP != nil || probers.Skills != nil || probers.KnowledgeBases != nil {
		t.Errorf("probers were built without their subsystems: %+v", probers)
	}
}
