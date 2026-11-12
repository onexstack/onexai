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

// Package chat is the oai-chat-apiserver implementation.
package chat

import (
	"context"
	"log/slog"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/onexstack/onexstack/pkg/server"
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/biz"
	"github.com/onexstack/onexai/internal/chat/biz/v1/dissolve"
	"github.com/onexstack/onexai/internal/chat/handler"
	"github.com/onexstack/onexai/internal/chat/mcpserver"
	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/oidc"
	"github.com/onexstack/onexai/internal/pkg/readiness"
	"github.com/onexstack/onexai/pkg/dbschema"
	customoptions "github.com/onexstack/onexai/pkg/options"
	"github.com/onexstack/onexai/pkg/wshub"
)

const serviceName = "oai-chat-apiserver"

// Config contains application-related configurations.
//
// Every *Options block is non-nil by construction: ServerOptions constructs
// each one with its NewXxxOptions default, and the config decoder only overlays
// fields, so a key left out (or even written as null) keeps the pointer. The
// providers therefore read these blocks directly and do not nil-check them.
type Config struct {
	SecureServingOptions   *genericoptions.SecureServingOptions
	InsecureServingOptions *genericoptions.InsecureServingOptions
	CoreDBOptions          *customoptions.CoreDBOptions
	FakeOptions            *genericoptions.RestyOptions
	OssOptions             *genericoptions.RestyOptions

	// LarkOptions holds Lark/Feishu application credentials.
	LarkOptions *customoptions.LarkOptions
	// AgentOptions configures the oai-agent-apiserver client.
	AgentOptions *genericoptions.RestyOptions
	// AgentAPIKey / AgentSecretKey are the AK/SK pair this server signs
	// its oai-agent-apiserver requests with. oai-agent-apiserver authenticates every /v1
	// call, including the agent stream that carries approval decisions, so an
	// empty pair here means no agent turn can start.
	AgentAPIKey    string
	AgentSecretKey string
	// BackendOptions configures the auxiliary HTTP-backed clients (CMDB,
	// Zadig, Prometheus).
	BackendOptions *customoptions.BackendOptions
	// DutyOptions selects the on-call/incident provider.
	DutyOptions *customoptions.DutyOptions
	// ChatOptions configures session lifecycle, streaming, and the agent
	// backends.
	ChatOptions *customoptions.ChatOptions

	// MCPOptions configures the MCP listener that publishes this service's
	// commands to oai-agent-apiserver's agents. Nil or disabled means no listener.
	MCPOptions *customoptions.MCPOptions

	// FeatureOptions gates the os-apiserver alignment switches. Nil and
	// all-false are equivalent: every switch is off by default.
	FeatureOptions *customoptions.FeatureOptions

	// ControlPlaneOptions points the service at oai-declserver. The kubeconfig
	// is the switch: the resources that moved to the control plane have no other
	// reader.
	ControlPlaneOptions *customoptions.ControlPlaneOptions

	// StoreOptions selects how the control-plane resources are read: the informer
	// cache, or the PostgreSQL projection.
	StoreOptions *customoptions.StoreOptions

	// JWTSecret is the HMAC secret for signing JWT tokens.
	JWTSecret string
	// OIDCSigner issues OIDC-compatible access tokens, or nil when the
	// deployment has not enabled OIDC. It is additive: the HS256 token remains
	// the browser credential.
	OIDCSigner *oidc.Signer
	// AnonymousAuth allows unauthenticated requests.
	AnonymousAuth bool
	// AuthorizationMode controls how authenticated endpoints are protected.
	AuthorizationMode string
}

// ServerConfig contains the core dependencies and configurations of the server.
type ServerConfig struct {
	*Config
	Handler *handler.Handler
	Biz     biz.IBiz
	// Readiness answers /readyz: whether the dependencies this instance needs
	// after start-up are still reachable.
	Readiness *readiness.Registry
	// Dissolve sweeps closed ticket groups out of existence. Nil when the
	// deployment has not opted in, which is the default: the call is
	// irreversible and takes the group's history with it.
	Dissolve dissolve.Sweeper
}

// Server represents the web server and its background workers.
type Server struct {
	cfg      *ServerConfig
	srv      server.Server
	hub      *wshub.Hub
	sessions *session.Manager
	// mcp is the MCP listener that publishes this service's commands to
	// oai-agent-apiserver's agents. Nil when the deployment has not enabled it.
	mcp *mcpserver.Bridge
}

// New creates and returns a new Server instance.
func (cfg *Config) New(ctx context.Context) (*Server, error) {
	// Create the core server instance using dependency injection.
	// This relies on the wire-generated NewServer function.
	s, err := NewServer(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return s.Prepare(ctx)
}

// Prepare performs post-initialization tasks. Lark event reception
// (webhook/websocket) is started by the handler transport layer during REST
// route installation.
func (s *Server) Prepare(ctx context.Context) (*Server, error) {
	_ = metrics.Initialize(ctx, serviceName, s.cfg.FeatureOptions.AlignedMetricNames)

	// Start the WebSocket hub event loop (relays IM messages to TUI clients).
	go s.hub.Run()

	// Close conversations that have gone idle. Without this the session map holds
	// every conversation the bot has ever replied in, for the life of the process.
	go s.sessions.RunReaper(ctx)

	// Dissolve closed ticket groups. The bot already tells the group it will
	// happen — "本群将在 1 天后自动解散" (this group will be dissolved automatically in one day) — and until this existed nothing did it.
	if s.cfg.Dissolve != nil {
		go s.cfg.Dissolve.Run(ctx)
	}

	// Drop spent binding claims. Nothing consumes one on read, so this table
	// only grows; see store.RunPeerBindClaimSweeper for why that is.
	go store.RunPeerBindClaimSweeper(ctx, s.cfg.Biz.Store().PeerBindClaims(), store.PeerBindClaimSweepInterval)

	// The MCP listener serves agents rather than people: it is not on the request
	// path of any chat, and a deployment that fails to bind it still answers
	// Lark. So its failure is logged and the process carries on — taking the
	// whole bot down over an optional side-channel would be the worse outcome.
	if s.mcp != nil {
		go func() {
			if err := s.mcp.Start(ctx); err != nil {
				slog.Error("MCP listener stopped", "error", err)
			}
		}()
	}

	return s, nil
}

// Run starts the HTTP server and gracefully shuts it down on ctx cancellation.
func (s *Server) Run(ctx context.Context) error {
	return server.Serve(ctx, s.srv)
}

func (cfg *Config) NewDB() (*gorm.DB, error) {
	slog.Info("initializing database connection", "dialect", cfg.CoreDBOptions.Dialect)
	dbInstance, err := cfg.CoreDBOptions.NewDB()
	if err != nil {
		slog.Error("failed to create database connection", "error", err)
		return nil, err
	}

	if dbschema.Current() == dbschema.MySQL {
		// Create tables with utf8mb4 so user-generated text (Chinese, emoji, etc.)
		// can be stored. The connection DSN is utf8mb3-only; without an explicit
		// charset the tables would inherit the (often latin1) database default and
		// reject CJK content with MySQL error 1366.
		//
		// PostgreSQL has no table-level charset to configure, so none of this
		// applies there.
		dbInstance = dbInstance.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci")
	}

	// Automatically migrate database schema
	if err := registry.Migrate(dbInstance); err != nil {
		slog.Error("failed to migrate database schema", "error", err)
		return nil, err
	}

	if dbschema.Current() == dbschema.MySQL {
		// Upgrade tables created by older deployments (before utf8mb4 was the
		// default). The conversion is idempotent and re-encodes any latin1 columns.
		// The metadata table is excluded: its data column is a MySQL JSON type
		// (stored as utf8mb4 internally), which CONVERT TO CHARACTER SET skips.
		convertTables := []string{
			"onexai_chat_message_record",
			"onexai_chat_session",
			"onexai_chat_ticket",
		}
		for _, table := range convertTables {
			if err := dbInstance.Exec("ALTER TABLE `" + table + "` CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci").Error; err != nil {
				// A missing table is benign (it may simply not be registered yet).
				slog.Debug("failed to convert table charset", "table", table, "error", err)
			}
		}
	}

	return dbInstance, nil
}
