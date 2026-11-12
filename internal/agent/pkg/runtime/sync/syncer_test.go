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

package sync

import (
	"context"
	"testing"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime"
	agentpkg "github.com/onexstack/onexai/pkg/agentapi"
	agentapi "github.com/onexstack/onexai/pkg/apis/agent"
)

type fakeProfiles struct {
	rows []*model.RuntimeProfileM
}

func (f *fakeProfiles) Create(context.Context, *model.RuntimeProfileM) error { return nil }
func (f *fakeProfiles) Update(context.Context, *model.RuntimeProfileM) error { return nil }
func (f *fakeProfiles) Delete(context.Context, *where.Options) error         { return nil }
func (f *fakeProfiles) Get(context.Context, *where.Options) (*model.RuntimeProfileM, error) {
	return nil, nil
}
func (f *fakeProfiles) List(context.Context, *where.Options) (int64, []*model.RuntimeProfileM, error) {
	return int64(len(f.rows)), f.rows, nil
}

type fakeHostings struct {
	rows []*model.RuntimeHostingM
}

func (f *fakeHostings) Create(context.Context, *model.RuntimeHostingM) error { return nil }
func (f *fakeHostings) Update(context.Context, *model.RuntimeHostingM) error { return nil }
func (f *fakeHostings) Delete(context.Context, *where.Options) error         { return nil }
func (f *fakeHostings) Get(context.Context, *where.Options) (*model.RuntimeHostingM, error) {
	return nil, nil
}
func (f *fakeHostings) List(context.Context, *where.Options) (int64, []*model.RuntimeHostingM, error) {
	return int64(len(f.rows)), f.rows, nil
}

type fakePort struct{ name string }

func (f fakePort) Name() string { return f.name }
func (f fakePort) Kind() string { return "a2a" }
func (f fakePort) Card(context.Context) (*a2atype.AgentCard, error) {
	return nil, nil
}
func (f fakePort) Run(context.Context, *runtime.Request) (<-chan agentpkg.Event, error) {
	return nil, nil
}
func (f fakePort) Cancel(context.Context, string) error { return nil }

func TestRefreshRegistersInprocUnderProfileName(t *testing.T) {
	fallback := fakePort{name: "eino"}
	registry := runtime.NewRegistry(fallback)
	profiles := &fakeProfiles{rows: []*model.RuntimeProfileM{{
		Name: "eino-inproc", Kind: agentapi.RuntimeKindInproc, Status: model.StatusEnabled,
	}}}

	syncer := &Syncer{Registry: registry, Profiles: profiles}
	syncer.Refresh(context.Background())

	port, err := registry.Lookup("eino-inproc")
	require.NoError(t, err)
	assert.Equal(t, "eino", port.Name())
}

func TestRefreshRegistersHostingInstancesPerAgent(t *testing.T) {
	registry := runtime.NewRegistry(fakePort{name: "eino"})
	profiles := &fakeProfiles{rows: []*model.RuntimeProfileM{{
		Name: "kagent", Kind: agentapi.RuntimeKindA2A, Status: model.StatusEnabled,
		HostingRef: "pool",
	}}}
	hostings := &fakeHostings{rows: []*model.RuntimeHostingM{{
		Name: "pool", ProfileRef: "kagent", Status: model.StatusEnabled,
		Instances: `[{"id":"i1","agent":"a1","endpoint":"http://127.0.0.1:18080","state":"ready"}]`,
	}}}
	syncer := &Syncer{Registry: registry, Profiles: profiles, Hostings: hostings}
	syncer.Refresh(context.Background())

	port, err := registry.LookupFor("kagent", "a1")
	require.NoError(t, err)
	assert.Equal(t, "kagent/a1", port.Name())

	_, err = registry.LookupFor("kagent", "a2")
	require.Error(t, err)
}

func TestRefreshRemovesDisabledAndUnresolved(t *testing.T) {
	registry := runtime.NewRegistry(fakePort{name: "eino"})
	profiles := &fakeProfiles{rows: []*model.RuntimeProfileM{
		{Name: "off", Kind: agentapi.RuntimeKindA2A, Status: model.StatusDisabled},
		{Name: "empty", Kind: agentapi.RuntimeKindA2A, Status: model.StatusEnabled},
	}}

	syncer := &Syncer{Registry: registry, Profiles: profiles}
	syncer.Refresh(context.Background())

	_, err := registry.Lookup("off")
	require.Error(t, err)
	_, err = registry.Lookup("empty")
	require.Error(t, err)
}
