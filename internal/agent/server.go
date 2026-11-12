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

// Package agent is the oai-agent-apiserver implementation.
package agent

import (
	"context"
	"log/slog"

	"github.com/redis/go-redis/v9"
	"k8s.io/apiserver/pkg/authentication/authenticator"

	"github.com/onexstack/onexstack/pkg/server"

	"github.com/onexstack/onexai/internal/agent/biz/v1/mediagc"
	"github.com/onexstack/onexai/internal/agent/biz/v1/sessiongc"
	"github.com/onexstack/onexai/internal/agent/capability"
	"github.com/onexstack/onexai/internal/agent/di"
	"github.com/onexstack/onexai/internal/agent/dispatcher"
	"github.com/onexstack/onexai/internal/agent/handler"
	"github.com/onexstack/onexai/internal/agent/pkg/authn"
	"github.com/onexstack/onexai/internal/agent/pkg/controlplane"
	"github.com/onexstack/onexai/internal/agent/pkg/memory"
	runtimesync "github.com/onexstack/onexai/internal/agent/pkg/runtime/sync"
	"github.com/onexstack/onexai/internal/agent/pkg/sandbox"
	"github.com/onexstack/onexai/internal/agent/pkg/taskstore"
	"github.com/onexstack/onexai/internal/pkg/idempotent"
	"github.com/onexstack/onexai/internal/pkg/readiness"
)

const serviceName = "oai-agent-apiserver"

// Config is the server's configuration. It is declared in package di so
// that the providers can read it without importing this package; the alias
// keeps the historical agent.Config name working for callers.
type Config = di.Config

// ServerConfig contains the core dependencies and configurations of the server.
//
// It is assembled by wire.Struct from the providers, so every field here is a
// dependency someone provides — there is no hand-written constructor to keep in
// sync with it.
type ServerConfig struct {
	*Config
	Handler *handler.Handler
	// TokenAuthenticator is the Kubernetes OIDC authenticator built from
	// auth.authentication-config, or nil when no file is configured. It is tried
	// before the platform's HS256 token so an OIDC token is accepted without
	// changing the browser credential.
	TokenAuthenticator authenticator.Request
	// AgentCall authenticates Agent invocation entries with the target Agent's
	// authentication Webhooks. Nil means no such dependency was built, which is
	// the zero-configuration behavior: the platform chain is always used.
	AgentCall *authn.AgentCall
	// Redis is the process's one client, or nil when none is configured. It is
	// carried here rather than rebuilt where the router needs it, so that the
	// nonce store uses the same pool, the same timeouts and the same address as
	// everything else. A second client would be a second thing to configure
	// correctly.
	Redis *redis.Client
	// Nonce carries the replay protection the /v1 signature check consults. It
	// is injected rather than built inside the router for the same reason Redis
	// is: one construction, one configuration, and the router only wires what it
	// is handed.
	Nonce authn.NonceStore
	// MediaGC reclaims stored images whose deadline has passed. Nil until an
	// operator sets a retention, which is the default: deleting a picture is not
	// recoverable, so the machinery waits for the policy rather than picking one.
	MediaGC mediagc.Collector
	// Idempotency records the key of a mutating request so a retry cannot run
	// the same write twice.
	Idempotency idempotent.Store
	// Readiness answers /readyz: whether the dependencies this instance needs
	// after start-up are still reachable. Empty when nothing can be probed, in
	// which case the endpoint reports ready — the process either started or it
	// did not.
	Readiness *readiness.Registry
	// Health republishes the live health of the resources the runtime talks to.
	// Nil when no control plane is configured, which is the same condition that
	// leaves the declarative resources unavailable.
	Health *controlplane.HealthReporter
	// Capability is the runtime-only MCP surface. Nil disables the internal
	// listener, which is what a deployment with no remote runtimes has.
	Capability *capability.Handler
	// RuntimeSyncer keeps remote ports aligned with RuntimeProfile objects.
	RuntimeSyncer *runtimesync.Syncer
	// SessionGC closes sessions idle past their TTL.
	SessionGC *sessiongc.Collector
	// RunDispatcher resumes asynchronous run admissions left pending by a crash.
	RunDispatcher *dispatcher.RunDispatcher
	// Sandbox is the container execution subsystem, or nil when disabled.
	Sandbox *sandbox.Service
	// TaskStore is the durable A2A task contract for remote runtimes.
	TaskStore *taskstore.HTTPHandler
	// TokenRefresh renews a run's capability token while the run is in flight.
	// Nil leaves the token at the lifetime the run was issued with.
	TokenRefresh *capability.TokenRefreshHandler
	// MemoryWorker expires old memories and purges soft-deleted rows. Nil
	// disables the maintenance loop, which is the case when no database is
	// configured.
	MemoryWorker *memory.Service
}

// Server represents the web server and its background workers.
type Server struct {
	cfg *ServerConfig
	srv server.Server
}

// New creates and returns a new Server instance.
func New(ctx context.Context, cfg *Config) (*Server, error) {
	// Create the core server instance using dependency injection.
	// This relies on the wire-generated NewServer function.
	s, err := NewServer(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Reclaim expired images. Started here rather than in Run so it shares the
	// server's lifetime: a collector outliving the process it belongs to would
	// delete against a store whose configuration it no longer matches.
	//
	// It is started in the constructor rather than in a Prepare step because a
	// second phase that always succeeds and only starts one goroutine is a step
	// every caller has to remember and no test can fail on.
	if s.cfg.MediaGC != nil {
		go s.cfg.MediaGC.Run(ctx)
	}
	if s.cfg.Health != nil {
		go s.cfg.Health.Run(ctx)
	}
	if s.cfg.Sandbox != nil {
		go func() {
			if err := s.cfg.Sandbox.RunReaper(ctx); err != nil {
				slog.WarnContext(ctx, "sandbox reaper stopped", "error", err)
			}
		}()
	}
	if s.cfg.SessionGC != nil {
		go func() {
			if err := s.cfg.SessionGC.Run(ctx); err != nil {
				slog.WarnContext(ctx, "idle session worker stopped", "error", err)
			}
		}()
	}
	if s.cfg.RunDispatcher != nil {
		go func() {
			if err := s.cfg.RunDispatcher.Run(ctx); err != nil {
				slog.WarnContext(ctx, "run dispatcher stopped", "error", err)
			}
		}()
	}
	if s.cfg.RuntimeSyncer != nil {
		go func() {
			if err := s.cfg.RuntimeSyncer.Run(ctx); err != nil {
				slog.WarnContext(ctx, "runtime syncer stopped", "error", err)
			}
		}()
	}
	if s.cfg.MemoryWorker != nil {
		go func() {
			if err := s.cfg.MemoryWorker.Run(ctx); err != nil {
				slog.WarnContext(ctx, "memory maintenance worker stopped", "error", err)
			}
		}()
	}
	return s, nil
}

// Run starts the HTTP server and gracefully shuts it down on ctx cancellation.
func (s *Server) Run(ctx context.Context) error {
	return server.Serve(ctx, s.srv)
}

// ServerConfig is assembled by wire.Struct in wire.go: its fields are exactly
// the providers a server needs, so no hand-written constructor can drift from
// it.

// NewWebServer creates and returns a new web server instance using the provided server configuration.
func NewWebServer(serverConfig *ServerConfig) (server.Server, error) {
	return serverConfig.NewGinServer()
}
