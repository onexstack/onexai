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

package skill

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"

	"github.com/onexstack/onexai/internal/agent/model"
)

// fakeProviderName is the provider the probe tests register. It is not one of
// the real sources: what is under test is the probe's error handling, and a real
// provider would make that depend on the network.
const fakeProviderName = "__probe_test__"

var registerFakeProviderOnce sync.Once

// fakeProbeBackend answers List from the record's own name, so one fake covers
// success and failure. The embedded nil interface is the rest of FullBackend: a
// probe must never reach for those methods, and a panic is the right way to
// learn that it did.
type fakeProbeBackend struct {
	FullBackend
	count int
	err   error
}

func (b *fakeProbeBackend) List(context.Context) ([]adkskill.FrontMatter, error) {
	if b.err != nil {
		return nil, b.err
	}
	out := make([]adkskill.FrontMatter, b.count)
	return out, nil
}

func registerFakeProvider(t *testing.T) {
	t.Helper()
	registerFakeProviderOnce.Do(func() {
		Register(ProviderSpec{
			Name: fakeProviderName,
			Build: func(_ context.Context, s *model.SkillM, _ BuildSpec) (FullBackend, error) {
				switch s.Name {
				case "unbuildable":
					return nil, errors.New("the record is malformed")
				case "broken":
					return &fakeProbeBackend{err: errors.New("the source answered 404")}, nil
				default:
					return &fakeProbeBackend{count: 2}, nil
				}
			},
		})
	})
}

func probeManager(t *testing.T) *Manager {
	t.Helper()
	registerFakeProvider(t)
	return NewManager(t.TempDir(), "", ProviderDeps{})
}

func probeRecord(name string) model.SkillM {
	return model.SkillM{Name: name, Status: model.StatusEnabled, Provider: fakeProviderName}
}

func TestProbe_ReportsHowManySkillsTheSourceHolds(t *testing.T) {
	m := probeManager(t)
	record := probeRecord("ok")

	count, err := m.Probe(context.Background(), record)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if count != 2 {
		t.Errorf("Probe counted %d skills, want 2", count)
	}
}

// TestProbe_ReportsWhyTheSourceFailed pins the difference from BuildAll: the run
// path swallows a per-record failure, a probe reports it.
func TestProbe_ReportsWhyTheSourceFailed(t *testing.T) {
	m := probeManager(t)

	if _, err := m.Probe(context.Background(), probeRecord("broken")); err == nil ||
		!strings.Contains(err.Error(), "404") {
		t.Errorf("a source error is not reported: %v", err)
	}
	if _, err := m.Probe(context.Background(), probeRecord("unbuildable")); err == nil ||
		!strings.Contains(err.Error(), "malformed") {
		t.Errorf("a build error is not reported: %v", err)
	}

	unknown := probeRecord("ok")
	unknown.Provider = "no-such-provider"
	if _, err := m.Probe(context.Background(), unknown); err == nil ||
		!strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("an unknown provider is not reported: %v", err)
	}
}

func TestProbe_RefusesADisabledRecord(t *testing.T) {
	m := probeManager(t)
	record := probeRecord("ok")
	record.Status = model.StatusDisabled

	if _, err := m.Probe(context.Background(), record); err == nil {
		t.Error("probing a disabled skill must report an error rather than a count")
	}
}

// TestProbe_ReusesTheBackendAcrossProbes pins that a probe interval does not
// become a download interval: the second probe of the same record reuses the
// backend the first one built, whose archive cache is what bounds the fetching.
func TestProbe_ReusesTheBackendAcrossProbes(t *testing.T) {
	m := probeManager(t)
	record := probeRecord("ok")

	if _, err := m.Probe(context.Background(), record); err != nil {
		t.Fatalf("first probe: %v", err)
	}
	key := backendCacheKey(probeAgentKey, record.Name)
	first, ok := m.cached(key, fingerprintOf(record))
	if !ok {
		t.Fatal("the first probe did not cache its backend")
	}

	if _, err := m.Probe(context.Background(), record); err != nil {
		t.Fatalf("second probe: %v", err)
	}
	second, ok := m.cached(key, fingerprintOf(record))
	if !ok || first != second {
		t.Error("the second probe rebuilt the backend instead of reusing it")
	}
}
