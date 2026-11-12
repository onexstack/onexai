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
	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/agent/handler"
	"github.com/onexstack/onexai/internal/agent/store/sqlstore"
)

// The sets below live next to the providers they name. Adding a provider means
// editing one of these groups rather than a list in another package, and a new
// group does not have to be threaded through the injector by hand.
var (
	// InfraSet is everything a process needs before any domain object exists:
	// the database, Redis and the stores built on them, plus the switches and
	// metrics they are governed by.
	InfraSet = wire.NewSet(
		ProvideDB,
		sqlstore.ProviderSet,
		ProvideAIStore,
		ProvideRedisClient,
		ProvideReadiness,
		ProvideIdempotencyStore,
		ProvideNonceStore,
		ProvideSessionStore,
		ProvideStopRegistry,
		ProvideCheckpointStore,
		ProvideFeatureOptions,
		ProvideMetrics,
		ProvideRateLimiter,
		ProvideTokenLimiter,
	)

	// ControlPlaneSet is the declarative control plane and the background work
	// that publishes what only the runtime can observe. The probers sit here
	// because they exist only to feed that report.
	ControlPlaneSet = wire.NewSet(
		ProvideDeclClient,
		ProvideSettingStore,
		ProvideHealthProbers,
		ProvideHealthReporter,
		ProvideExtensions,
	)

	// AuthzSet is the authorization subsystem and the two administrable tables
	// that steer it. They are one group because the rule store, the policy
	// engine and the coordinator share one reload path.
	AuthzSet = wire.NewSet(
		ProvideAuthzRuleSource,
		ProvideAuthzRuleStore,
		ProvideAuthzOPAAuthorizer,
		ProvideAuthzOPACache,
		ProvideAuthzCoordinator,
		ProvideAgentWebhookAuthz,
		ProvideAuthzRules,
		ProvideTokenAuthenticator,
		ProvideAgentCall,
		ProvideAuthReview,
		ProvideWebhooks,
		ProvideWebhookDispatcher,
	)

	// MediaSet is image storage, generation and reclamation.
	MediaSet = wire.NewSet(
		ProvideMediaBundle,
		ProvideAssetStore,
		ProvideImageGenerator,
		ProvideImageDrawer,
		ProvideImageInliner,
		ProvideAssetRetention,
		ProvideMediaCollector,
	)

	// AgentSet is the agent runtime and the HTTP surface that exposes it. The
	// handler's Deps are assembled field by field (see wire.Struct) so that a
	// capability is added by providing it, not by widening a constructor.
	AgentSet = wire.NewSet(
		ProvideSkillManager,
		ProvideContextSkillSource,
		ProvideToolRegistry,
		ProvideMCPManager,
		ProvideCustomCommandBiz,
		ProvideKBToolSource,
		ProvideRetrievalAudit,
		ProvideAgentEngine,
		ProvideRuntimeRegistry,
		ProvideRuntimeSyncer,
		ProvideRunStore,
		ProvideRunDispatcher,
		ProvideMemoryService,
		ProvideSessionGC,
		ProvideScheduledExecutionStore,
		ProvideSandboxService,
		ProvideTaskStoreHandler,
		ProvideCapabilityHandler,
		ProvideCapabilityIssuer,
		ProvideDelegationHolder,
		ProvideAgentProvider,
		ProvideTokenRefresh,
		ProvideMemoryProvider,
		ProvideMemoryTools,
		ProvideAgentBiz,
		ProvideInteractionWriter,
		ProvideK8SGPTRouter,
		ProvideA2AHandlers,
		ProvideScreener,
		ProvideOutputCaps,
		ProvideModelConfig,
		ProvideKnowledgeBaseConfig,
		ProvideAccessKeyConfig,
		ProvideDecisionCallers,
		ProvideResourceGuard,
		ProvideAuthzRulesAdmin,
		ProvideWebhooksAdmin,
		ProvideMediaHandler,
		wire.Struct(new(handler.Deps), "*"),
		handler.NewHandler,
	)

	// ProviderSet is every provider this service has. The injector in package
	// agent names only this and the business-layer set.
	ProviderSet = wire.NewSet(InfraSet, ControlPlaneSet, AuthzSet, MediaSet, AgentSet)
)
