package biz

import (
	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/agent/biz/v1/agent"
	"github.com/onexstack/onexai/internal/agent/biz/v1/agentbinding"
	"github.com/onexstack/onexai/internal/agent/biz/v1/agenttemplate"
	"github.com/onexstack/onexai/internal/agent/biz/v1/aksk"
	"github.com/onexstack/onexai/internal/agent/biz/v1/asset"
	"github.com/onexstack/onexai/internal/agent/biz/v1/authzrule"
	"github.com/onexstack/onexai/internal/agent/biz/v1/clusteragentbinding"
	"github.com/onexstack/onexai/internal/agent/biz/v1/customcommand"
	"github.com/onexstack/onexai/internal/agent/biz/v1/imagegen"
	"github.com/onexstack/onexai/internal/agent/biz/v1/knowledgebase"
	"github.com/onexstack/onexai/internal/agent/biz/v1/mcp"
	memorybiz "github.com/onexstack/onexai/internal/agent/biz/v1/memory"
	"github.com/onexstack/onexai/internal/agent/biz/v1/model"
	"github.com/onexstack/onexai/internal/agent/biz/v1/namespace"
	"github.com/onexstack/onexai/internal/agent/biz/v1/observability"
	"github.com/onexstack/onexai/internal/agent/biz/v1/prompt"
	"github.com/onexstack/onexai/internal/agent/biz/v1/referencegrant"
	runbiz "github.com/onexstack/onexai/internal/agent/biz/v1/run"
	"github.com/onexstack/onexai/internal/agent/biz/v1/runtimehosting"
	"github.com/onexstack/onexai/internal/agent/biz/v1/runtimeprofile"
	sandboxbiz "github.com/onexstack/onexai/internal/agent/biz/v1/sandbox"
	"github.com/onexstack/onexai/internal/agent/biz/v1/sandboxtemplate"
	"github.com/onexstack/onexai/internal/agent/biz/v1/scheduledrun"
	"github.com/onexstack/onexai/internal/agent/biz/v1/secret"
	sharebiz "github.com/onexstack/onexai/internal/agent/biz/v1/share"
	"github.com/onexstack/onexai/internal/agent/biz/v1/skill"
	"github.com/onexstack/onexai/internal/agent/biz/v1/usergroup"
	"github.com/onexstack/onexai/internal/agent/biz/v1/webhook"
	"github.com/onexstack/onexai/internal/agent/pkg/agentauthz"
	"github.com/onexstack/onexai/internal/agent/pkg/k8sgpt"
	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	memorysvc "github.com/onexstack/onexai/internal/agent/pkg/memory"
	sandboxsvc "github.com/onexstack/onexai/internal/agent/pkg/sandbox"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// ProviderSet defines dependency injection rules for the business logic layer.
var ProviderSet = wire.NewSet(NewBiz, wire.Bind(new(IBiz), new(*biz)), asset.ProviderSet, imagegen.ProviderSet)

// IBiz is the top-level business logic interface.
type IBiz interface {
	// Agent returns the AI agent run-time business logic.
	Agent() agentapi.IAgentBiz
	// AgentV1 returns the declarative Agent resource business logic.
	AgentV1() agent.AgentResourceBiz
	// AgentBindingV1 returns the agent-binding business logic.
	AgentBindingV1() agentbinding.AgentBindingBiz
	// ClusterAgentBindingV1 returns the cluster-wide binding business logic.
	ClusterAgentBindingV1() clusteragentbinding.Biz
	// ReferenceGrantV1 returns the cross-namespace sharing-grant business logic.
	ReferenceGrantV1() referencegrant.ReferenceGrantBiz
	// AccessKeyV1 returns the personal credential business logic.
	AccessKeyV1() aksk.AccessKeyBiz
	// UserGroupV1 returns the user-group management business logic.
	UserGroupV1() usergroup.UserGroupBiz
	// AgentTemplateV1 returns the agent-template business logic.
	AgentTemplateV1() agenttemplate.AgentTemplateBiz
	// AuthzRuleV1 returns the authorization-rule business logic.
	AuthzRuleV1() authzrule.AuthzRuleBiz
	// Asset returns the media asset business logic.
	Asset() asset.AssetBiz
	// CustomCommandV1 returns the custom-command business logic.
	CustomCommandV1() customcommand.CustomCommandBiz
	// ImageGen returns the image-generation business logic.
	ImageGen() imagegen.ImageGenBiz
	// KnowledgeBaseV1 returns the knowledge-base business logic.
	KnowledgeBaseV1() knowledgebase.KnowledgeBaseBiz
	// MCPV1 returns the MCP server business logic.
	MCPV1() mcp.MCPBiz
	// MemoryV1 returns the memory administration logic.
	MemoryV1() memorybiz.MemoryBiz
	// ModelV1 returns the model-endpoint business logic.
	ModelV1() model.ModelBiz
	// NamespaceV1 returns the business-line business logic.
	NamespaceV1() namespace.NamespaceBiz
	// ObservabilityV1 returns the cluster observability logic.
	ObservabilityV1() observability.ObservabilityBiz
	// PromptV1 returns the Prompt business logic.
	PromptV1() prompt.PromptBiz
	// RunV1 returns the run/session/revision query logic.
	RunV1() runbiz.RunBiz
	// RuntimeProfileV1 returns the runtime-profile business logic.
	RuntimeProfileV1() runtimeprofile.RuntimeProfileBiz
	// RuntimeHostingV1 returns the runtime-hosting business logic.
	RuntimeHostingV1() runtimehosting.RuntimeHostingBiz
	// SandboxV1 returns the sandbox administration logic.
	SandboxV1() sandboxbiz.SandboxBiz
	// SandboxTemplateV1 returns the sandbox-template business logic.
	SandboxTemplateV1() sandboxtemplate.SandboxTemplateBiz
	// ScheduledRunV1 returns the scheduled-run business logic.
	ScheduledRunV1() scheduledrun.ScheduledRunBiz
	// ScheduledExecutionV1 returns the scheduled-execution business logic.
	ScheduledExecutionV1() scheduledrun.ExecutionBiz
	// SecretV1 returns the Secret business logic.
	SecretV1() secret.SecretBiz
	// ShareV1 returns the session-share business logic.
	ShareV1() sharebiz.ShareBiz
	// SkillV1 returns the Skill business logic.
	SkillV1() skill.SkillBiz
	// WebhookV1 returns the run-lifecycle hook business logic.
	WebhookV1() webhook.WebhookBiz
}

// biz is the concrete implementation of the business logic IBiz.
//
// The per-resource sub-businesses are constructed once here rather than on
// every accessor call: a handler resolves its collaborator once per request
// today, and rebuilding the same stateless struct each time was only cost.
type biz struct {
	agentBiz              agentapi.IAgentBiz
	agentResourceBiz      agent.AgentResourceBiz
	agentBindingBiz       agentbinding.AgentBindingBiz
	clusterBindingBiz     clusteragentbinding.Biz
	referenceGrantBiz     referencegrant.ReferenceGrantBiz
	accessKeyBiz          aksk.AccessKeyBiz
	userGroupBiz          usergroup.UserGroupBiz
	agentTemplateBiz      agenttemplate.AgentTemplateBiz
	assetBiz              asset.AssetBiz
	authzRuleBiz          authzrule.AuthzRuleBiz
	customCommandBiz      customcommand.CustomCommandBiz
	imageBiz              imagegen.ImageGenBiz
	knowledgeBaseBiz      knowledgebase.KnowledgeBaseBiz
	mcpBiz                mcp.MCPBiz
	memoryBiz             memorybiz.MemoryBiz
	modelBiz              model.ModelBiz
	namespaceBiz          namespace.NamespaceBiz
	observabilityBiz      observability.ObservabilityBiz
	promptBiz             prompt.PromptBiz
	runBiz                runbiz.RunBiz
	runtimeProfileBiz     runtimeprofile.RuntimeProfileBiz
	runtimeHostingBiz     runtimehosting.RuntimeHostingBiz
	sandboxBiz            sandboxbiz.SandboxBiz
	sandboxTemplateBiz    sandboxtemplate.SandboxTemplateBiz
	scheduledRunBiz       scheduledrun.ScheduledRunBiz
	scheduledExecutionBiz scheduledrun.ExecutionBiz
	secretBiz             secret.SecretBiz
	shareBiz              sharebiz.ShareBiz
	skillBiz              skill.SkillBiz
	webhookBiz            webhook.WebhookBiz
}

// Ensure biz implements IBiz at compile time.
var _ IBiz = (*biz)(nil)

// NewBiz constructs a biz instance from its dependencies. Each sub-business is
// built once, here, so the accessors below are field reads.
func NewBiz(
	agentBiz agentapi.IAgentBiz,
	assetBiz asset.AssetBiz,
	imageBiz imagegen.ImageGenBiz,
	customCommandBiz customcommand.CustomCommandBiz,
	store agentstore.AIStore,
	skillSource skill.ContextSkillSource,
	modelConfig *model.Config,
	knowledgeBaseConfig *knowledgebase.Config,
	accessKeyConfig *aksk.Config,
	authzRules authzrule.AuthzRuleStore,
	webhooks webhook.WebhookStore,
	runs runstore.Store,
	executions agentstore.ScheduledExecutionStore,
	memories *memorysvc.Service,
	sandbox *sandboxsvc.Service,
	k8sgpt *k8sgpt.Router,
	mcpProber *mcpclient.Manager,
	agentWebhookAuthz *agentauthz.Resolver,
) *biz {
	return &biz{
		agentBiz:              agentBiz,
		agentResourceBiz:      agent.NewResource(store, agentWebhookAuthz, mcpProber),
		agentBindingBiz:       agentbinding.New(store, mcpProber),
		clusterBindingBiz:     clusteragentbinding.New(store, mcpProber),
		referenceGrantBiz:     referencegrant.New(store),
		accessKeyBiz:          aksk.New(store, accessKeyConfig),
		userGroupBiz:          usergroup.New(store),
		agentTemplateBiz:      agenttemplate.New(store, mcpProber),
		assetBiz:              assetBiz,
		authzRuleBiz:          authzrule.New(authzRules),
		customCommandBiz:      customCommandBiz,
		imageBiz:              imageBiz,
		knowledgeBaseBiz:      knowledgebase.New(store, knowledgeBaseConfig),
		mcpBiz:                mcp.New(store, mcpProber),
		memoryBiz:             memorybiz.New(store, memories),
		modelBiz:              model.New(store, modelConfig),
		namespaceBiz:          namespace.New(store),
		observabilityBiz:      observability.New(k8sgpt),
		promptBiz:             prompt.New(store),
		runBiz:                runbiz.New(runs, agentBiz),
		runtimeProfileBiz:     runtimeprofile.New(store),
		runtimeHostingBiz:     runtimehosting.New(store),
		sandboxBiz:            sandboxbiz.New(sandbox),
		sandboxTemplateBiz:    sandboxtemplate.New(store),
		scheduledRunBiz:       scheduledrun.New(store),
		scheduledExecutionBiz: scheduledrun.NewExecution(executions),
		secretBiz:             secret.New(store),
		shareBiz:              sharebiz.New(runs),
		skillBiz:              skill.New(store, skillSource),
		webhookBiz:            webhook.New(webhooks),
	}
}

// Agent returns the AI agent run-time business logic.
func (b *biz) Agent() agentapi.IAgentBiz { return b.agentBiz }

// AgentV1 returns the declarative Agent resource business logic.
func (b *biz) AgentV1() agent.AgentResourceBiz { return b.agentResourceBiz }

// AgentBindingV1 returns the agent-binding business logic.
func (b *biz) AgentBindingV1() agentbinding.AgentBindingBiz { return b.agentBindingBiz }

// ClusterAgentBindingV1 returns the cluster-wide binding business logic.
func (b *biz) ClusterAgentBindingV1() clusteragentbinding.Biz { return b.clusterBindingBiz }

// ReferenceGrantV1 returns the cross-namespace sharing-grant business logic.
func (b *biz) ReferenceGrantV1() referencegrant.ReferenceGrantBiz { return b.referenceGrantBiz }

// AccessKeyV1 returns the personal credential business logic.
func (b *biz) AccessKeyV1() aksk.AccessKeyBiz { return b.accessKeyBiz }

// UserGroupV1 returns the user-group management business logic.
func (b *biz) UserGroupV1() usergroup.UserGroupBiz { return b.userGroupBiz }

// AgentTemplateV1 returns the agent-template business logic.
func (b *biz) AgentTemplateV1() agenttemplate.AgentTemplateBiz { return b.agentTemplateBiz }

// AuthzRuleV1 returns the authorization-rule business logic.
func (b *biz) AuthzRuleV1() authzrule.AuthzRuleBiz { return b.authzRuleBiz }

// Asset returns the media asset business logic.
func (b *biz) Asset() asset.AssetBiz { return b.assetBiz }

// CustomCommandV1 returns the custom-command business logic.
func (b *biz) CustomCommandV1() customcommand.CustomCommandBiz { return b.customCommandBiz }

// ImageGen returns the image-generation business logic.
func (b *biz) ImageGen() imagegen.ImageGenBiz { return b.imageBiz }

// KnowledgeBaseV1 returns the knowledge-base business logic.
func (b *biz) KnowledgeBaseV1() knowledgebase.KnowledgeBaseBiz { return b.knowledgeBaseBiz }

// MCPV1 returns the MCP server business logic.
func (b *biz) MCPV1() mcp.MCPBiz { return b.mcpBiz }

// MemoryV1 returns the memory administration logic.
func (b *biz) MemoryV1() memorybiz.MemoryBiz { return b.memoryBiz }

// ModelV1 returns the model-endpoint business logic.
func (b *biz) ModelV1() model.ModelBiz { return b.modelBiz }

// NamespaceV1 returns the business-line business logic.
func (b *biz) NamespaceV1() namespace.NamespaceBiz { return b.namespaceBiz }

// ObservabilityV1 returns the cluster observability logic.
func (b *biz) ObservabilityV1() observability.ObservabilityBiz { return b.observabilityBiz }

// PromptV1 returns the Prompt business logic.
func (b *biz) PromptV1() prompt.PromptBiz { return b.promptBiz }

// RunV1 returns the run/session/revision query logic.
func (b *biz) RunV1() runbiz.RunBiz { return b.runBiz }

// RuntimeProfileV1 returns the runtime-profile business logic.
func (b *biz) RuntimeProfileV1() runtimeprofile.RuntimeProfileBiz { return b.runtimeProfileBiz }

// RuntimeHostingV1 returns the runtime-hosting business logic.
func (b *biz) RuntimeHostingV1() runtimehosting.RuntimeHostingBiz { return b.runtimeHostingBiz }

// SandboxV1 returns the sandbox administration logic.
func (b *biz) SandboxV1() sandboxbiz.SandboxBiz { return b.sandboxBiz }

// SandboxTemplateV1 returns the sandbox-template business logic.
func (b *biz) SandboxTemplateV1() sandboxtemplate.SandboxTemplateBiz { return b.sandboxTemplateBiz }

// ScheduledRunV1 returns the scheduled-run business logic.
func (b *biz) ScheduledRunV1() scheduledrun.ScheduledRunBiz { return b.scheduledRunBiz }

// ScheduledExecutionV1 returns the scheduled-execution business logic.
func (b *biz) ScheduledExecutionV1() scheduledrun.ExecutionBiz { return b.scheduledExecutionBiz }

// SecretV1 returns the Secret business logic.
func (b *biz) SecretV1() secret.SecretBiz { return b.secretBiz }

// ShareV1 returns the session-share business logic.
func (b *biz) ShareV1() sharebiz.ShareBiz { return b.shareBiz }

// SkillV1 returns the Skill business logic.
func (b *biz) SkillV1() skill.SkillBiz { return b.skillBiz }

// WebhookV1 returns the run-lifecycle hook business logic.
func (b *biz) WebhookV1() webhook.WebhookBiz { return b.webhookBiz }
