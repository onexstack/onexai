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
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	customoptions "github.com/onexstack/onexai/pkg/options"
)

// Config contains application-related configurations.
//
// It lives here, next to the providers that read it, because those providers
// are what a Config is for: the server package aliases it as agent.Config, so
// the two packages can be wired without importing each other.
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

	// StoreOptions selects/configures the AI-resource store backend.
	StoreOptions *customoptions.StoreOptions
	// ControlPlaneOptions points the service at oai-declserver; empty kubeconfig
	// means no control plane and an empty (fail-closed) rule set.
	ControlPlaneOptions *customoptions.ControlPlaneOptions
	// RedisOptions configures Redis for agent sessions / cross-pod stop signaling.
	RedisOptions *customoptions.RedisOptions
	// MediaOptions configures where image bytes are stored and how a remote
	// image is fetched. A disabled driver leaves the service able to answer
	// every text request and unable to store an image, which is a deployment's
	// decision rather than a broken state.
	MediaOptions *customoptions.MediaOptions
	// AgentOptions configures the AI agent flow (engine, concurrency, timeouts).
	AgentOptions *customoptions.AgentOptions
	// AuthzOptions configures the multi-level (Agent/Tool/Resource) authorization subsystem.
	AuthzOptions *customoptions.AuthzOptions
	// FeatureOptions gates the os-apiserver alignment switches. Every switch is
	// off by default.
	FeatureOptions *customoptions.FeatureOptions

	// BypassAKSK disables inbound AK/SK signature verification on /v1.
	BypassAKSK bool
	// ControlToken is the second gate on /v1/core/secrets and /v1/agent/authorizationrules.
	// Empty disables it (the pre-existing behaviour).
	ControlToken string

	// JWTSecret is the HMAC secret for signing JWT tokens.
	JWTSecret string
	// WebhookToken is the shared Bearer credential a Kubernetes webhook caller
	// presents to the TokenReview/SubjectAccessReview endpoints. Empty disables
	// those endpoints rather than exposing them unauthenticated.
	WebhookToken string
	// AuthenticationConfig is the path to a Kubernetes
	// apiserver.config.k8s.io/v1 AuthenticationConfiguration. Empty keeps the
	// platform's HS256-only credential path.
	AuthenticationConfig string
	// LLMBaseURL is the base URL for the OpenAI-compatible LLM API.
	LLMBaseURL string
	// LLMAPIKey is the API key for the LLM service.
	LLMAPIKey string
	// LLMModel is the model name for LLM requests.
	LLMModel string
	// LLMMaxTokens caps one completion's output, or 0 for no cap of our own.
	// Per-surface ceilings from agent.max-output-tokens override it; see
	// handler.OutputCaps.
	LLMMaxTokens int
	// LLMAllowPrivateEndpoints permits user-configured endpoints to reach
	// private/loopback addresses. See options.LLMOptions.
	LLMAllowPrivateEndpoints bool
	// LLMTestTimeout bounds one connectivity test of a model endpoint.
	LLMTestTimeout time.Duration
	// ModelAPIKeyEncryptionKey encrypts model endpoint API keys at rest.
	ModelAPIKeyEncryptionKey string
	// UsageReportOptions names the ledger finished runs are reported to. An
	// unconfigured endpoint (Enabled() false) means this deployment reports
	// nowhere. See usagereport.
	UsageReportOptions *customoptions.UsageReportOptions
	// AnonymousAuth allows unauthenticated requests.
	AnonymousAuth bool
	// AuthorizationMode controls how authenticated endpoints are protected.
	AuthorizationMode string
	// Addr is the HTTP listen address.
	Addr string
}

// NewDB opens the database described by the configuration and migrates the
// schemas registered with the store registry.
func (cfg *Config) NewDB() (*gorm.DB, error) {
	slog.Info("initializing database connection", "dialect", cfg.CoreDBOptions.Dialect)
	dbInstance, err := cfg.CoreDBOptions.NewDB()
	if err != nil {
		slog.Error("failed to create database connection", "error", err)
		return nil, err
	}

	// Automatically migrate database schema
	if err := registry.Migrate(dbInstance); err != nil {
		slog.Error("failed to migrate database schema", "error", err)
		return nil, err
	}

	return dbInstance, nil
}
