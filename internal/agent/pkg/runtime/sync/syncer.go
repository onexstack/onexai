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

// Package sync keeps the runtime registry aligned with the RuntimeProfile
// objects. It is the only writer of remote ports: a profile that is disabled,
// deleted, or has no resolved endpoint is removed, so a run cannot reach a
// runtime the control plane no longer declares.
package sync

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime"
	runtimea2a "github.com/onexstack/onexai/internal/agent/pkg/runtime/a2a"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/clientconfig"
	agentapi "github.com/onexstack/onexai/pkg/apis/agent"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// DefaultInterval is how often the syncer reconciles. A profile edit is not
// hot: the next run after this window sees it, which is the same guarantee the
// runner cache gives.
const DefaultInterval = 30 * time.Second

// Syncer reconciles the registry with the profile store.
type Syncer struct {
	Registry *runtime.Registry
	Profiles store.RuntimeProfileStore
	Hostings store.RuntimeHostingStore
	Interval time.Duration
}

// Run reconciles immediately and then on the interval until ctx is done.
func (s *Syncer) Run(ctx context.Context) error {
	if s.Registry == nil || s.Profiles == nil {
		return nil
	}
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	s.Refresh(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.Refresh(ctx)
		}
	}
}

// Refresh registers every usable profile and unregisters the rest.
// instancesByProfile reads each hosting's status and groups its instances by
// the profile the hosting serves.
func (s *Syncer) instancesByProfile(ctx context.Context) map[string][]agentv1alpha1.RuntimeInstanceStatus {
	out := make(map[string][]agentv1alpha1.RuntimeInstanceStatus)
	if s.Hostings == nil {
		return out
	}
	_, hostings, err := s.Hostings.List(ctx, nil)
	if err != nil {
		slog.WarnContext(ctx, "runtime sync: list hostings", "error", err)
		return out
	}
	for _, hosting := range hostings {
		if hosting == nil || hosting.ProfileRef == "" || hosting.Instances == "" {
			continue
		}
		var instances []agentv1alpha1.RuntimeInstanceStatus
		if err := json.Unmarshal([]byte(hosting.Instances), &instances); err != nil {
			continue
		}
		out[hosting.ProfileRef] = append(out[hosting.ProfileRef], instances...)
	}
	return out
}

func (s *Syncer) Refresh(ctx context.Context) {
	if s.Registry == nil || s.Profiles == nil {
		return
	}
	instancesByProfile := s.instancesByProfile(ctx)
	_, profiles, err := s.Profiles.List(ctx, nil)
	if err != nil {
		slog.WarnContext(ctx, "runtime sync: list profiles", "error", err)
		return
	}
	keep := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if profile == nil {
			continue
		}
		name := profile.Name
		switch {
		case profile.Status == model.StatusDisabled:
			s.Registry.Unregister(name)
			continue
		case profile.Kind == "" || profile.Kind == agentapi.RuntimeKindInproc:
			if port := s.Registry.Default(); port != nil {
				s.Registry.Register(name, port)
				keep[name] = struct{}{}
			}
			continue
		case profile.Kind != agentapi.RuntimeKindA2A:
			s.Registry.Unregister(name)
			continue
		}
		endpoint := profile.ResolvedEndpoint
		if endpoint == "" {
			endpoint = profile.Endpoint
		}
		var httpClient *http.Client
		if profile.ClientConfigJSON != "" {
			cfg := &agentv1alpha1.ClientConfig{}
			if err := json.Unmarshal([]byte(profile.ClientConfigJSON), cfg); err != nil {
				slog.WarnContext(ctx, "runtime sync: decode clientConfig", "profile", name, "error", err)
				s.Registry.Unregister(name)
				continue
			}
			resolved, err := clientconfig.Resolve(*cfg)
			if err != nil {
				slog.WarnContext(ctx, "runtime sync: resolve clientConfig", "profile", name, "error", err)
				s.Registry.Unregister(name)
				continue
			}
			endpoint = resolved.URL
			httpClient, err = clientconfig.HTTPClient(*cfg)
			if err != nil {
				slog.WarnContext(ctx, "runtime sync: build clientConfig client", "profile", name, "error", err)
				s.Registry.Unregister(name)
				continue
			}
		}
		// A hosting-backed profile has one endpoint per agent. Registering the
		// profile-wide endpoint as a fallback would route an agent with no
		// instance to another agent's runtime, so it is deliberately absent.
		if profile.HostingRef == "" {
			if endpoint == "" {
				s.Registry.Unregister(name)
				continue
			}
			port, err := runtimea2a.New(runtimea2a.Config{
				Name:       name,
				Endpoint:   endpoint,
				HTTPClient: httpClient,
			})
			if err != nil {
				slog.WarnContext(ctx, "runtime sync: build a2a port", "profile", name, "error", err)
				s.Registry.Unregister(name)
				continue
			}
			s.Registry.Register(name, port)
		}
		for _, instance := range instancesByProfile[name] {
			if instance.Agent == "" || instance.Endpoint == "" || instance.State != "ready" {
				continue
			}
			agentPort, err := runtimea2a.New(runtimea2a.Config{
				Name:       name + "/" + instance.Agent,
				Endpoint:   instance.Endpoint,
				HTTPClient: httpClient,
			})
			if err != nil {
				continue
			}
			s.Registry.RegisterFor(name, instance.Agent, agentPort)
		}
		keep[name] = struct{}{}
	}
	for _, name := range s.Registry.Names() {
		if _, ok := keep[name]; ok {
			continue
		}
		// Do not unregister the built-in default, which is not a profile but is
		// listed under the name the deployment may have registered it with.
		if s.Registry.Default() != nil && name == s.Registry.Default().Name() {
			continue
		}
		s.Registry.Unregister(name)
	}
}
