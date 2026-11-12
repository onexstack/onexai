//go:build wireinject
// +build wireinject

package chat

import (
	"context"

	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/chat/biz"
	"github.com/onexstack/onexai/internal/chat/biz/v1/agentcommand"
	"github.com/onexstack/onexai/internal/chat/biz/v1/bot"
	"github.com/onexstack/onexai/internal/chat/biz/v1/chat"
	"github.com/onexstack/onexai/internal/chat/biz/v1/command"
	"github.com/onexstack/onexai/internal/chat/biz/v1/oncall"
	"github.com/onexstack/onexai/internal/chat/biz/v1/oncallgroup"
	"github.com/onexstack/onexai/internal/chat/biz/v1/ticket"
	"github.com/onexstack/onexai/internal/chat/pkg/agentcmd"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/validation"
	agentclient "github.com/onexstack/onexai/pkg/clientset/agent"
)

// infrastructureSet groups all infrastructure-related providers.
// This keeps the main wire.Build call clean.
var infrastructureSet = wire.NewSet(
	ProvideDB,
	ProvideReadiness,
	ProvideDeclClient,
	ProvideLarkClient,
	ProvideClientset,
	// The store is built by ProvideStore, which composes the control-plane
	// accessors with the SQL ones; store.ProviderSet would add a second IStore.
	ProvideStore,
	ProvideDutyProvider,
	ProvideDutyStore,
	ProvideEscalationEngine,
	ProvideDutyScheduleBiz,
	ProvideUserResolver,
	ProvideDutyService,
	ProvideSessionManager,
	ProvideAgentRegistry,
	ProvideAgentCommandResolver,
	ProvideRoleResolver,
	ProvideRoleAuthz,
	ProvideStaticRuntime,
	ProvideMCPServer,
	validation.ProviderSet,
	bot.ProviderSet,
	ProvideLarkBackend,
	ProvideLarkOAuth,
	ProvideAuthBiz,
	ProvideLarkProfileResolver,
	ProvideUserBiz,
	ProvideHub,
	ProvideMessageStore,
	ProvideAgentClient,
	ProvideDissolveSweeper,
	// The chat biz reads an image's bytes through one method of the agent
	// client. Binding the narrow interface here rather than passing the whole
	// client keeps that package's dependency on it to the single call it makes.
	wire.Bind(new(chat.Assets), new(agentclient.Interface)),
	// The on-call read surface reads history through one method of the chat biz
	// rather than through the biz interface, for the same reason as the line
	// above: a package that needs one conversation lookup should not have to
	// fake a dozen methods to be tested.
	ProvideOncallHistory,
	chat.ProviderSet,
	oncallgroup.ProviderSet,
	ticket.ProviderSet,
	oncall.ProviderSet,
	agentcommand.ProviderSet,
	command.ProviderSet,
	wire.Bind(new(runtime.AgentCommandResolver), new(*agentcmd.Resolver)),
)

// NewServer initializes and creates the web server with all necessary dependencies using Wire.
func NewServer(ctx context.Context, cfg *Config) (*Server, error) {
	wire.Build(
		// Server infrastructure
		NewWebServer,
		wire.Struct(new(Server), "*"),

		// Domain layers
		biz.ProviderSet,
		ProvideHandler,
		ProvideServerConfig,

		// Infrastructure dependencies
		infrastructureSet,
	)
	return nil, nil
}
