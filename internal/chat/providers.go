package chat

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/server"
	"golang.org/x/time/rate"
	"gorm.io/gorm"
	klabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"


	"github.com/onexstack/onexai/internal/agent/pkg/controlplane"
	"github.com/onexstack/onexai/internal/chat/biz"
	authn "github.com/onexstack/onexai/internal/chat/biz/v1/auth"
	chatbiz "github.com/onexstack/onexai/internal/chat/biz/v1/chat"
	"github.com/onexstack/onexai/internal/chat/biz/v1/dissolve"
	"github.com/onexstack/onexai/internal/chat/biz/v1/dutyschedule"
	"github.com/onexstack/onexai/internal/chat/biz/v1/oncall"
	userbiz "github.com/onexstack/onexai/internal/chat/biz/v1/user"
	"github.com/onexstack/onexai/internal/chat/commandreg"
	"github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/dutyservice"
	"github.com/onexstack/onexai/internal/chat/escalation"
	"github.com/onexstack/onexai/internal/chat/handler"
	imlark "github.com/onexstack/onexai/internal/chat/im/lark"
	"github.com/onexstack/onexai/internal/chat/mcpserver"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	cliagent "github.com/onexstack/onexai/internal/chat/pkg/agent/cli"
	llmagent "github.com/onexstack/onexai/internal/chat/pkg/agent/llm"
	"github.com/onexstack/onexai/internal/chat/pkg/agentcmd"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/cmdb"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/lark"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/prometheus"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/zadig"
	"github.com/onexstack/onexai/internal/chat/pkg/dnswhitelist"
	"github.com/onexstack/onexai/internal/chat/pkg/i18n"
	"github.com/onexstack/onexai/internal/chat/pkg/larkoauth"
	"github.com/onexstack/onexai/internal/chat/pkg/roleauthz"
	"github.com/onexstack/onexai/internal/chat/pkg/roleset"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	"github.com/onexstack/onexai/internal/chat/pkg/stream"
	"github.com/onexstack/onexai/internal/chat/pkg/validation"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/chat/store/controlplanestore"
	"github.com/onexstack/onexai/internal/chat/userdir"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/authz/opa"
	"github.com/onexstack/onexai/internal/pkg/larkkit"
	"github.com/onexstack/onexai/internal/pkg/readiness"
	agentclient "github.com/onexstack/onexai/pkg/clientset/agent"
	"github.com/onexstack/onexai/pkg/declclient"
	publicduty "github.com/onexstack/onexai/pkg/duty"
	regenprovider "github.com/onexstack/onexai/pkg/duty/regen"
	agentlisters "github.com/onexstack/onexai/pkg/generated/listers/agent/v1alpha1"
	customoptions "github.com/onexstack/onexai/pkg/options"
	"github.com/onexstack/onexai/pkg/wshub"
)

// ProvideLarkClient builds the Lark SDK client from the configured credentials.
func ProvideLarkClient(cfg *Config) lark.Interface {
	return lark.NewForConfig(cfg.LarkOptions.AppID, cfg.LarkOptions.AppSecret)
}

// ProvideClientset builds the cross-service clientset.
//
// The oai-agent-apiserver client is injected rather than constructed here: it must be
// the same signed client the agent backend uses, because oai-agent-apiserver refuses
// an unsigned /v1 call. Building a second, unsigned one here is how every
// command and card that reaches the server through the clientset — the agent
// stream included — used to come back 401.
func ProvideClientset(cfg *Config, larkcli lark.Interface, agentcli agentclient.Interface) clientset.Interface {
	return clientset.New(
		larkcli,
		agentcli,
		cmdb.NewClient(cfg.BackendOptions.CMDBEndpoint, cfg.BackendOptions.CMDBSecretID, cfg.BackendOptions.CMDBSecretKey),
		zadig.NewClient(cfg.BackendOptions.ZadigEndpoint, cfg.BackendOptions.ZadigToken, cfg.BackendOptions.ZadigProject),
		prometheus.NewClient(cfg.BackendOptions.PrometheusEndpoint),
	)
}

// ProvideDutyProvider selects the duty backend configured for this deployment.
// The returned provider is shared by the roster store and the ticket mirror so
// both use the same session.
func ProvideDutyProvider(cfg *Config) publicduty.Provider {
	provider := cfg.DutyOptions.EffectiveProvider(cfg.BackendOptions)
	switch provider {
	case "regen":
		return regenprovider.NewClient(
			cfg.BackendOptions.RegenEndpoint,
			cfg.BackendOptions.RegenUsername,
			cfg.BackendOptions.RegenPassword,
		)
	case "none":
		return publicduty.NewNoop("none")
	default:
		slog.Warn("chat: unknown duty provider, the duty store stays empty", "provider", provider)
		return publicduty.NewNoop("none")
	}
}

// dutyRefreshInterval is how often the in-memory duty store is re-read from the
// provider. Rotations turn over weekly, so this only bounds how long a schedule
// edit takes to become visible to the ChatOps commands.
const dutyRefreshInterval = 5 * time.Minute

// ProvideDutyStore creates the in-memory legacy duty/on-call store. Ticket
// persistence is owned by the store layer (store.Ticket); the duty store keeps
// only the in-memory Regen roster.
//
// It is no longer what the commands read for a normal group: ProvideDutyService
// reads the native schedule tables live. The store remains the fallback for
// groups whose schedule_id still points at Regen, and it still refreshes in the
// background so that fallback is ready when it is needed. A failed load is
// logged and tolerated — a provider outage must not stop this service from
// starting — and a deployment that configures no provider keeps an empty store.
// ProvideStore builds the chat store: command grants come from the declarative
// control plane, everything else from SQL.
//
// The control plane is required. A deployment without one has no source for the
// migrated resources, and a second SQL reader would be a second truth.
func ProvideDeclClient(ctx context.Context, cfg *Config) (*declclient.Client, error) {
	controlPlane := cfg.ControlPlaneOptions
	if controlPlane == nil || !controlPlane.Enabled() {
		return nil, fmt.Errorf("chat: the control plane is not configured; set controlplane.kubeconfig")
	}

	client, err := declclient.New(controlPlane.Kubeconfig, controlPlane.Namespace, controlPlane.ResyncPeriod,
		declclient.WithInformerFilter(cfg.StoreOptions.InformerFilter()))
	if err != nil {
		return nil, fmt.Errorf("chat: build the control plane client: %w", err)
	}
	if err := client.Start(ctx, controlPlane.SyncTimeout); err != nil {
		return nil, fmt.Errorf("chat: control plane caches did not sync: %w", err)
	}

	// Publish the compiled command catalogue. It runs off the request path and
	// off start-up: an unreachable control plane must not stop the bot from
	// answering Lark, and the periodic leg repairs a hand-deleted object.
	runtime.GoSafe(ctx, "chat.commandreg", func() {
		commandreg.Run(ctx, client, controlPlane.Namespace)
	})
	return client, nil
}

func ProvideStore(cfg *Config, db *gorm.DB, client *declclient.Client) (store.IStore, error) {
	if client == nil {
		return nil, fmt.Errorf("chat: the store needs the declarative control plane; set controlplane.kubeconfig")
	}
	namespace := ""
	if cfg.ControlPlaneOptions != nil {
		namespace = cfg.ControlPlaneOptions.Namespace
	}
	slog.Info("store: command grants and peer bindings come from the control plane; duty groups and schedules come from PostgreSQL", "namespace", namespace)
	if cfg.StoreOptions != nil && cfg.StoreOptions.UsesMirror() {
		slog.Info("store: reading from the PostgreSQL projection",
			"kinds", cfg.StoreOptions.MirroredKinds(), "schema", cfg.StoreOptions.Mirror.Schema)
	}
	return controlplanestore.NewStore(store.NewStore(db), client, namespace,
		&controlplanestore.MirrorConfig{DB: db, Options: cfg.StoreOptions, Namespace: namespace})
}

// ProvideDutyScheduleBiz builds the native schedule biz. The duty store is
// passed twice through two narrow roles: the legacy roster answers for groups
// that still point at Regen, and the refresher makes a schedule edit visible to
// /duty immediately rather than on the next five-minute tick.
func ProvideDutyScheduleBiz(
	st store.IStore, ds *duty.Store, engine *escalation.Engine,
) dutyschedule.DutyScheduleBiz {
	return dutyschedule.New(st, dutyLegacyRoster{store: ds}, ds, engine)
}

// ProvideEscalationEngine builds the escalation engine and, when the deployment
// opts in, starts its worker. Manual escalation uses the same engine whether or
// not the worker runs.
func ProvideEscalationEngine(
	ctx context.Context, cfg *Config, st store.IStore, cs clientset.Interface, resolve userdir.Resolver,
) *escalation.Engine {
	interval := 30 * time.Second
	enabled := false
	if cfg.DutyOptions != nil {
		if cfg.DutyOptions.EscalationInterval > 0 {
			interval = cfg.DutyOptions.EscalationInterval
		}
		enabled = cfg.DutyOptions.EscalationWorkerEnabled
	}

	engine := escalation.New(st, escalation.NewLarkNotifier(cs.L()), resolve, interval, enabled)
	if enabled {
		slog.InfoContext(ctx, "escalation: worker enabled", "interval", interval)
		runtime.GoSafe(ctx, "chat.escalation", func() { engine.Run(ctx) })
	}
	return engine
}

// dutyLegacyRoster adapts the in-memory duty store to the schedule biz's
// LegacyRoster interface.
type dutyLegacyRoster struct {
	store *duty.Store
}

func (r dutyLegacyRoster) LegacyWatchers(group string) ([]string, bool) {
	if r.store == nil {
		return nil, false
	}
	info, ok := r.store.GetGroupInfo(group)
	if !ok {
		return nil, false
	}
	names := make([]string, 0, len(info.Watchers))
	for _, watcher := range info.Watchers {
		if watcher != nil && watcher.UserName != "" {
			names = append(names, watcher.UserName)
		}
	}
	return names, true
}

// ProvideUserResolver builds the on-call name -> Lark user_id resolver shared by
// the duty service and the escalation notifier.
//
// backend.regen-user-map stays the explicit override; a name that misses it is
// looked up in the account table and its Feishu binding. Caching is what keeps
// the duty refresh and the escalation worker from querying per ticket.
func ProvideUserResolver(ctx context.Context, cfg *Config, st store.IStore) userdir.Resolver {
	overrides, err := cfg.BackendOptions.ParseRegenUserMap()
	if err != nil {
		slog.WarnContext(ctx, "chat: invalid on-call user map, explicit overrides disabled", "error", err)
		overrides = nil
	}
	var users userdir.Store
	if st != nil {
		users = st.Users()
	}
	return userdir.NewDirectory(users, userdir.Static(overrides), 5*time.Minute, time.Minute)
}

// ProvideDutyService builds the command-facing duty capability: the native
// schedule service is the implementation, and the in-memory store is kept as
// the fallback for groups that still point at a legacy Regen schedule.
func ProvideDutyService(
	st store.IStore, legacy *duty.Store, biz dutyschedule.DutyScheduleBiz, resolve userdir.Resolver,
) duty.Service {
	return dutyservice.New(st.OncallGroups(), st.OncallSchedules(), legacy, biz, resolve)
}

func ProvideDutyStore(
	ctx context.Context, cfg *Config, st store.IStore, dutyProvider publicduty.Provider, resolve userdir.Resolver,
) *duty.Store {
	if !dutyProvider.Configured() {
		// Native schedules do not need a provider, so the store is still built.
		// The warning remains because a group that still points at a legacy
		// Regen schedule will answer "unavailable" until it is imported.
		slog.WarnContext(ctx, "chat: no duty provider is configured; native schedules still load, legacy Regen groups will not",
			"provider", dutyProvider.Name())
	}

	return duty.NewWithSource(ctx,
		duty.NewModuleSourceWithSchedules(st.OncallGroups(), st.OncallSchedules(), dutyProvider, resolve),
		dutyRefreshInterval)
}

// ProvideMCPServer builds the MCP listener that publishes this service's
// commands to oai-agent-apiserver's agents.
//
// A nil server and a nil error is the ordinary result for a deployment that
// never turned the listener on: it is a feature that has to be asked for, not a
// dependency that failed. Consumers check the server for nil and start nothing.
func ProvideMCPServer(cfg *Config, sr *runtime.StaticRuntime) (*mcpserver.Bridge, error) {
	return mcpserver.New(cfg.MCPOptions, sr)
}

// ProvideAgentCommandResolver builds the resolver that answers which commands a
// conversation's agents were given.
//
// It returns the concrete type rather than the interface it is bound to in
// Wire: the admin surface needs the resolver's own Preview and Invalidate, and
// a provider that returned only the interface would leave those reachable only
// through a type assertion.
func ProvideAgentCommandResolver(ctx context.Context, cfg *Config, cs clientset.Interface, client *declclient.Client) *agentcmd.Resolver {
	opts := agentcmd.Options{
		// Commands are granted by both binding kinds: a namespaced AgentBinding
		// and a cluster-wide ClusterAgentBinding. The projections are a plain
		// union, exactly like the two binding controllers merge them for an
		// agent's status.
		Rules: controlplanestore.NewRuleStore(
			controlplanestore.NewAgentBindingRuleStore(client),
			controlplanestore.NewClusterAgentBindingRuleStore(client),
		),
		Agents:  agentLister{agents: agentListerFrom(client)},
		Skills:  skillSource{client: cs.Agent()},
		Customs: customSource{client: cs.Agent()},
	}
	// The TTLs come from the chat block, whose defaults pkg/options takes from
	// agentcmd's own constants, so the two cannot disagree about "the default".
	opts.RulesTTL = cfg.ChatOptions.AgentCommand.RulesTTL
	opts.SkillsTTL = cfg.ChatOptions.AgentCommand.SkillsTTL
	opts.CustomsTTL = cfg.ChatOptions.AgentCommand.CustomsTTL

	resolver := agentcmd.New(opts)

	// Warm the per-agent command sources before a message needs them, off the
	// request path and off start-up: the reads go to the agent service, and a
	// bot that answers Lark must not wait for them to finish booting. See
	// Resolver.Warm for why the first message after a restart used to be the
	// slowest one.
	lister := agentLister{agents: agentListerFrom(client)}
	runtime.GoSafe(ctx, "chat.agentcmd.warm", func() {
		infos, err := lister.ListAgentInfo(ctx)
		if err != nil {
			slog.WarnContext(ctx, "agent commands: could not warm the per-agent sources", "error", err)
			return
		}
		agents := make([]string, 0, len(infos))
		for _, info := range infos {
			agents = append(agents, info.Name)
		}
		resolver.Warm(ctx, agents)
	})

	// The listers are already watch-backed, so a binding change can invalidate
	// the snapshot immediately instead of waiting for the TTL. Both binding
	// kinds are watched: they contribute to the same rule list.
	for _, informer := range []cache.SharedIndexInformer{
		client.Factory.Agent().V1alpha1().AgentBindings().Informer(),
		client.Factory.Agent().V1alpha1().ClusterAgentBindings().Informer(),
	} {
		_, _ = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(any) { resolver.Invalidate() },
			UpdateFunc: func(any, any) { resolver.Invalidate() },
			DeleteFunc: func(any) { resolver.Invalidate() },
		})
	}
	return resolver
}

// skillSource answers the resolver's question about an agent's skill commands.
type skillSource struct {
	client agentclient.Interface
}

var _ agentcmd.SkillSource = skillSource{}

// SkillsOf implements agentcmd.SkillSource.
//
// The repository kind travels as a label on each command rather than as a
// filter here: "provider=lark_doc" is a selector an operator writes, so the
// resolver needs the kind in hand to match it, and asking the agent service to
// filter would put half of the matching on the other side of the wire.
func (s skillSource) SkillsOf(ctx context.Context, agent string) ([]runtime.SkillCommand, error) {
	skills, err := s.client.ContextSkills(ctx, agent)
	if err != nil {
		return nil, err
	}

	out := make([]runtime.SkillCommand, 0, len(skills))
	for _, sk := range skills {
		if sk.GetName() == "" {
			continue
		}
		out = append(out, runtime.SkillCommand{
			Name:        sk.GetName(),
			Description: sk.GetDescription(),
			Provider:    sk.GetProvider(),
		})
	}
	return out, nil
}

// customSource answers the resolver's question about an agent's user-defined
// commands.
type customSource struct {
	client agentclient.Interface
}

var _ runtime.CustomCommandSource = customSource{}

// CustomCommandsOf implements agentcmd.CustomCommandSource.
func (s customSource) CustomCommandsOf(ctx context.Context, agent string) ([]runtime.CustomCommand, error) {
	commands, err := s.client.ContextCustomCommands(ctx, agent)
	if err != nil {
		return nil, err
	}

	out := make([]runtime.CustomCommand, 0, len(commands))
	for _, c := range commands {
		if c.GetName() == "" {
			continue
		}
		out = append(out, agentcmd.ContextCustomCommandFromProto(c, agent))
	}
	return out, nil
}

// agentLister adapts the control plane's Agent cache to the shape the command
// resolver selects agents on.
//
// It reads the lister rather than the agent service's HTTP API: the labels a
// rule selects on are the object's own metadata.labels, and the lister is the
// same objects, kept current by a watch. Going through the agent service instead
// would be a second representation of one fact — the tags column it used to
// return was a projection of these labels — plus a network hop and a cache on
// top of a cache.
//
// The adapter exists so the resolver — the part with the matching rules in it —
// does not depend on the API type, and a field the matching starts using is a
// change in one place rather than an import that appears three packages away.
type agentLister struct {
	agents agentlisters.AgentLister
}

var _ agentcmd.AgentLister = agentLister{}

// agentListerFrom returns the lister over the control plane's agents, or nil
// when no control plane is configured. A nil lister answers "no agents", which
// is the fail-closed state every other unwired dependency gives.
func agentListerFrom(client *declclient.Client) agentlisters.AgentLister {
	if client == nil {
		return nil
	}
	return client.Agents()
}

// ListAgentInfo implements agentcmd.AgentLister.
func (l agentLister) ListAgentInfo(context.Context) ([]agentcmd.AgentInfo, error) {
	if l.agents == nil {
		return nil, nil
	}
	agents, err := l.agents.List(klabels.Everything())
	if err != nil {
		return nil, err
	}

	infos := make([]agentcmd.AgentInfo, 0, len(agents))
	for _, a := range agents {
		infos = append(infos, agentcmd.AgentInfo{
			Name:      a.Name,
			Namespace: a.Namespace,
			Labels:    a.Labels,
		})
	}
	return infos, nil
}

// ProvideStaticRuntime creates the static runtime with shared long-lived dependencies.
func ProvideStaticRuntime(cfg *Config, dutySvc duty.Service, st store.IStore, cs clientset.Interface, dutyProvider publicduty.Provider, hub *wshub.Hub, sessions *session.Manager, agents *agent.Registry, cmdResolver runtime.AgentCommandResolver, roles *roleset.Cache, roleAuthz roleauthz.Resolver, oncallBiz oncall.OncallBiz, engine *escalation.Engine) *runtime.StaticRuntime {
	// The commands reach tickets through the runtime, so wrapping the store here
	// mirrors every duty command's writes onto Regen without touching a single
	// command. Other consumers of the store only use sessions and messages.
	if dutyProvider.Configured() {
		st = store.NewProviderSyncedStore(st, dutyProvider)
	}

	// The defaults live in the options package, which NewServerOptions applies,
	// so this reads them from one place rather than restating them.
	streaming := cfg.ChatOptions.Streaming
	dedup := cfg.ChatOptions.Dedup
	peerBind := cfg.ChatOptions.PeerBind

	return &runtime.StaticRuntime{
		Duty:       dutySvc,
		Escalation: engine,
		Store:      st,
		Clientset:  cs,
		// The conversation reader `/close` summarizes from. It is the on-call biz
		// rather than the chat biz because the three-level fallback and its
		// provenance markers already live there, next to the read surface that
		// serves the same material to the downstream sync.
		Oncall:        oncallBiz,
		CMDBEnabled:   cfg.BackendOptions.CMDBEnabled,
		DNSWhitelist:  provideDNSWhitelist(cfg),
		Hub:           hub,
		Sessions:      sessions,
		Agents:        agents,
		AgentCommands: cmdResolver,
		RoleAuthz:     roleAuthz,
		Streaming: stream.Config{
			Interval:      streaming.Interval,
			MinDeltaChars: streaming.MinDeltaChars,
			MaxChars:      streaming.MaxChars,
		},
		StreamingEnabled:          streaming.Enabled,
		StreamingPrintFrequencyMs: streaming.PrintFrequencyMs,
		StreamingPrintStep:        streaming.PrintStep,
		StreamingPrintStrategy:    streaming.PrintStrategy,
		MaxEventAge:               time.Duration(dedup.MaxEventAgeSec) * time.Second,
		PeerBindClaimTTL:          peerBind.ClaimTTL,
		AppID:                     cfg.LarkOptions.AppID,
		AppSecret:                 cfg.LarkOptions.AppSecret,
		BotName:                   cfg.LarkOptions.BotName,
		Roles:                     roles,
		InternalDefaultAgent:      cfg.ChatOptions.InternalDefault,
		ExternalDefaultAgent:      cfg.ChatOptions.ExternalDefault,
		AgentAllocator:            cfg.ChatOptions.AgentAllocator,
		DutyMaintainerUserID:      cfg.BackendOptions.DutyMaintainerUserID,
		P4DeferGroups:             cfg.BackendOptions.ParseP4DeferGroups(),

		AllowFrom:        parseAllowSet(cfg.LarkOptions.AllowFrom),
		AllowChat:        parseAllowSet(cfg.LarkOptions.AllowChat),
		GroupOnly:        cfg.LarkOptions.GroupOnly,
		ReplyToTrigger:   cfg.LarkOptions.ReplyToTrigger,
		ThreadIsolation:  cfg.LarkOptions.ThreadIsolation,
		ImageBatchWindow: time.Duration(cfg.LarkOptions.ImageBatchWindowMs) * time.Millisecond,
		OutboundLimiter:  provideOutboundLimiter(cfg),
		Language:         replyLanguage(cfg),
	}
}

// replyLanguage resolves the configured reply language.
//
// An unrecognised tag cannot reach here: ChatOptions.Validate refuses it
// at startup. It is handled anyway, and falls back to the source language,
// because the alternative — carrying on with a Language that matches no catalog
// and no comparison anywhere — would answer every user in a language nobody
// asked for, with nothing in the logs to say why.
func replyLanguage(cfg *Config) i18n.Language {
	lang, ok := i18n.ParseLanguage(cfg.ChatOptions.I18n.Language)
	if !ok {
		slog.Warn("unknown chat.i18n.language, answering in the source language",
			"language", cfg.ChatOptions.I18n.Language)
		return i18n.Default
	}
	return lang
}

// ProvideAgentRegistry builds the set of backends a conversation can run on.
//
// The oai-agent-apiserver backend is registered unconditionally: it is the backend
// this service has always had, and nothing about it is optional. The local
// process backend is registered only when the configuration enables it, because
// it is the one configuration that turns a chat message into a process on this
// host — a trust decision, not a feature flag.
func ProvideAgentRegistry(cfg *Config, cs clientset.Interface) *agent.Registry {
	o := cfg.ChatOptions
	if o == nil {
		o = customoptions.NewChatOptions()
	}

	agents := []agent.Agent{llmagent.New(cs.Agent())}
	if cli := o.Agents.CLI; cli.Enabled {
		backend, err := cliagent.New(cliagent.Options{
			WorkspaceRoot:   cli.WorkspaceRoot,
			AllowedDirs:     cli.AllowedDirs,
			MaxSessions:     cli.MaxSessions,
			PermissionMode:  cli.PermissionMode,
			AllowedTools:    cli.AllowedTools,
			DisallowedTools: cli.DisallowedTools,
			Model:           cli.Model,
			TurnTimeout:     cli.TurnTimeout,
			MaxLineBytes:    cli.MaxLineBytes,
		})
		if err != nil {
			// ChatOptions.Validate refuses this configuration at startup, so
			// reaching here means the two disagree. Leaving the backend out is the
			// safe direction to be wrong in: a conversation that resolves to it
			// fails and says so, rather than running a process somewhere nobody
			// chose.
			slog.Error("not registering the CLI agent backend", "error", err)
		} else {
			agents = append(agents, backend)
		}
	}
	return agent.NewRegistry(o.Agents.DefaultBackend, agents...)
}

// ProvideSessionManager builds the per-conversation store: a generation-guarded
// busy lock plus a disk snapshot of each conversation's agent binding.
//
// It is deliberately not injected with store.IStore. A conversation snapshot is
// a single-writer file describing a running process, not shared domain data; and
// keeping it out of the store layer keeps this package free of that package's
// package-level handle, so a second Manager (in tests, or a future shard) does
// not silently share state with the first.
func ProvideSessionManager(ctx context.Context, cfg *Config, agents *agent.Registry) (*session.Manager, error) {
	dir, err := sessionDataDir(cfg)
	if err != nil {
		return nil, err
	}

	o := cfg.ChatOptions
	if o == nil {
		o = customoptions.NewChatOptions()
	}

	return session.NewManager(ctx, session.Options{
		DataDir:            dir,
		MaxQueuedMessages:  o.Session.MaxQueuedMessages,
		MaxConcurrentTurns: o.Session.MaxConcurrentTurns,
		IdleTimeout:        o.Session.IdleTimeout,
		TurnTimeout:        o.Session.TurnTimeout,
		StaleLockMaxHeld:   o.Session.BusyStaleLockMaxHeld,
		DefaultBackend:     o.Agents.DefaultBackend,
		// A conversation this manager forgets may still be a live process on a
		// backend. Telling the backends when it goes is what keeps an idle
		// conversation from leaving an agent running on this host forever.
		OnClosed: func(key string) {
			if err := agents.CloseSession(ctx, key); err != nil {
				// The conversation is already gone from here, so there is nothing
				// to retry and nothing to fail: the cost of this is a process
				// that lives until the agent itself gives up.
				slog.Warn("failed to release the agent session of a closed conversation",
					"conv_id", key, "error", err)
			}
		},
	}), nil
}

// sessionDataDir resolves the conversation-snapshot directory, defaulting to
// ~/.onexai/chat/sessions. That sits beside the configuration the app
// looks for, which cmd/oai-chat-apiserver/app/server.go pins with
// app.WithDirInHome(".onexai").
func sessionDataDir(cfg *Config) (string, error) {
	if cfg.ChatOptions.Session.DataDir != "" {
		return cfg.ChatOptions.Session.DataDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for session snapshots: %w", err)
	}
	return filepath.Join(home, ".onexai", "chat", "sessions"), nil
}

// InstanceLockPath is the file whose exclusive lock marks this host's
// chat as running.
//
// It sits inside the session data directory because that directory is what two
// instances would corrupt: each would reap the other's conversations, and both
// would write snapshots and agent workspaces under the same root. Deployments
// that shard by data directory therefore get one lock per shard, which is the
// right granularity — they share nothing that this lock protects.
func InstanceLockPath(cfg *Config) (string, error) {
	dir, err := sessionDataDir(cfg)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "instance.lock"), nil
}

// provideOutboundLimiter builds the outbound rate limiter, or nil when rate
// limiting is disabled (rate <= 0).
func provideOutboundLimiter(cfg *Config) *rate.Limiter {
	if cfg.LarkOptions.OutboundRateLimit <= 0 {
		return nil
	}
	burst := cfg.LarkOptions.OutboundRateBurst
	if burst <= 0 {
		burst = 1
	}
	return rate.NewLimiter(rate.Limit(cfg.LarkOptions.OutboundRateLimit), burst)
}

// parseAllowSet parses a comma-separated allow-list into a set. A "*" entry (or
// an empty/whitespace-only string) yields an empty set, meaning "allow all".
func parseAllowSet(s string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item == "*" {
			return nil
		}
		set[item] = struct{}{}
	}
	return set
}

// ProvideRoleResolver reads the SRE and admin lists and keeps them fresh.
//
// # What happens when the first read fails
//
// It does not stop the server. Regen being unreachable must not stop this
// service, and neither must a role table that cannot be read — the same
// judgement ProvideDutyStore makes. The consequence is severe enough to be said
// out loud, though: with no role lists nobody may run a restricted command,
// /doctor included, so the warning below is the only diagnosis available until
// the next refresh succeeds.
//
// The store is required rather than optional: without it there is nothing to
// read, and a cache built on nothing answers "no role" for everyone.
func ProvideRoleResolver(ctx context.Context, cfg *Config, client *declclient.Client) *roleset.Cache {
	// The role lists are RBAC bindings now. Reads go to the informer cache, so
	// the interval is zero: there is nothing to poll, and a revoked role takes
	// effect on the next message rather than on the next tick.
	namespace := ""
	if cfg.ControlPlaneOptions != nil {
		namespace = cfg.ControlPlaneOptions.Namespace
	}
	c := roleset.NewLive(roleset.NewRBACSource(namespace, client.ClusterRoleBindings(), client.RoleBindings(namespace)))
	// The first load is synchronous: a server that answers a message before its
	// role lists are read would refuse the very commands the deployment granted.
	// A failure does not stop the timer, so the lists recover without a restart.
	if err := c.Start(ctx); err != nil {
		slog.WarnContext(ctx,
			"chat: the role lists could not be read at start-up; every restricted command is refused until the first successful refresh",
			"error", err)
	}
	return c
}

// ProvideRoleAuthz answers the two role questions from AuthorizationRule
// objects when chat.role-authz.enabled is set, and from the chat.setting lists
// otherwise.
//
// # Why the rules come from the control plane
//
// The agent service reads its rules from AuthorizationRule objects. Reading the
// same rules from the SQL table as well made kubectl-authored rules invisible
// here, so one cluster had two answers to "who is an SRE". One resource, one
// source: the objects.
//
// # Why a failure stops the process when the switch is on
//
// An enabled switch that silently fell back to the lists would report a policy
// that nobody is being judged by. The same reasoning is why the agent service
// refuses to start without a rule source: the operator asked for policy, so a
// policy that cannot be built is a startup error, not a quiet downgrade.
func ProvideRoleAuthz(
	ctx context.Context, cfg *Config, client *declclient.Client, lists *roleset.Cache,
	agentClient agentclient.Interface,
) (roleauthz.Resolver, error) {
	opts := cfg.ChatOptions.RoleAuthz
	if !opts.Enabled {
		return roleauthz.New(lists, nil), nil
	}
	if client == nil {
		return nil, fmt.Errorf("chat.role-authz is enabled but no control-plane client is configured")
	}

	source := controlplane.NewSource(client)
	ruleStore := opa.NewRuleStore(source)
	if _, err := ruleStore.Sync(ctx); err != nil {
		// Survivable: the reloader below keeps trying, and until it succeeds
		// every role is refused (all NoOpinion), which is the fail-closed answer.
		slog.WarnContext(ctx, "chat.role-authz: initial rule load failed; every role is refused until it succeeds", "error", err)
	}
	policy, err := opa.NewAuthorizer(ctx, ruleStore)
	if err != nil {
		return nil, fmt.Errorf("chat.role-authz: build the policy: %w", err)
	}

	reload := func(ctx context.Context) error {
		changed, err := ruleStore.Sync(ctx)
		if err != nil {
			return err
		}
		// Re-preparing the query is the expensive half of a reload, so it only
		// happens when the rule set actually changed.
		if changed {
			return policy.Reload(ctx)
		}
		return nil
	}

	// The control plane's watch drives every reload; the ticker is a safety net
	// for a watch that silently stops delivering.
	go watchRoleRules(ctx, source.Changes(), reload, opts.RuleTableRefreshInterval)

	slog.InfoContext(ctx, "chat role authorization reads AuthorizationRule objects",
		"refresh", opts.RuleTableRefreshInterval, "roles", "sre,admin",
		"user_groups", agentClient != nil)
	return roleauthz.NewWithGroups(lists, authz.NewCoordinator(policy, policy, true), agentClient), nil
}

// watchRoleRules reloads the rule set on every change event, and on the
// configured interval as a fallback. A failed reload keeps the previous
// snapshot, so a transient API error does not revoke every role.
func watchRoleRules(ctx context.Context, changes <-chan struct{}, reload func(context.Context) error, interval time.Duration) {
	if interval <= 0 {
		interval = customoptions.DefaultRoleAuthzRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			if err := reload(ctx); err != nil {
				slog.WarnContext(ctx, "chat.role-authz: reloading the rule set failed; keeping the previous one", "error", err)
			}
		case <-ticker.C:
			if err := reload(ctx); err != nil {
				slog.WarnContext(ctx, "chat.role-authz: periodic rule reload failed; keeping the previous snapshot", "error", err)
			}
		}
	}
}

// provideDNSWhitelist builds the in-memory DNS whitelist when a remote
// forward.conf URL is configured; returns nil otherwise.
func provideDNSWhitelist(cfg *Config) *dnswhitelist.Whitelist {
	if cfg.BackendOptions.DNSWhitelistURL == "" {
		return nil
	}
	w, err := dnswhitelist.New(context.Background(), cfg.BackendOptions.DNSWhitelistURL, cfg.BackendOptions.DNSWhitelistToken, 10*time.Minute)
	if err != nil {
		return nil
	}
	return w
}

// ProvideLarkBackend builds the IM-sync Lark backend (SDK-backed) used by the
// chat biz.
func ProvideLarkBackend(larkcli lark.Interface) *imlark.Backend {
	return imlark.New(larkcli.C())
}

// ProvideHub creates the WebSocket hub shared by the chat biz and handler.
func ProvideHub(cfg *Config) *wshub.Hub {
	ws := cfg.ChatOptions.WebSocket
	return wshub.NewHub(
		wshub.WithAllowedOrigins(ws.AllowedOrigins),
		wshub.WithMaxConcurrentFrames(ws.MaxConcurrentFrames),
	)
}

// ProvideMessageStore returns the message cache store.
func ProvideMessageStore(st store.IStore) store.MessageStore {
	return st.Messages()
}

// ProvideDissolveSweeper builds the group-dissolution sweep, or nil when the
// deployment has not opted in.
//
// It is a provider rather than something the server assembles because it needs
// the IM backend, which the server does not otherwise reach: the server talks
// to conversations through the biz layer, and this one call — dissolve a group
// — has no place in that layer's vocabulary.
func ProvideDissolveSweeper(cfg *Config, b biz.IBiz, larkBackend *imlark.Backend) dissolve.Sweeper {
	opt := cfg.ChatOptions.Dissolve
	return dissolve.New(b.Store(), larkBackend, slog.Default(), dissolve.Options{
		Enabled:         opt.Enabled,
		GraceAfterClose: opt.GraceAfterClose,
		Interval:        opt.Interval,
		Batch:           opt.Batch,
		HistoryLimit:    opt.HistoryLimit,
	})
}

// ProvideAgentClient returns the oai-agent-apiserver client for agent chat.
//
// The client signs every request: oai-agent-apiserver refuses an unsigned /v1 call,
// and the agent stream is where a user's turn — and their approval decisions —
// actually happen. An unconfigured pair therefore does not degrade to an
// anonymous call, it fails; the options validation says so at startup.
func ProvideAgentClient(cfg *Config) agentclient.Interface {
	return agentclient.NewWithSigning(cfg.AgentOptions, cfg.AgentAPIKey, cfg.AgentSecretKey)
}

// ProvideAuthBiz creates the authentication business logic instance.
//
// The login providers are all off unless the deployment enabled one: a missing
// chat block, and therefore a config that predates these switches, leaves every
// provider disabled rather than re-enabling the old accept-anything behaviour.
//
// The roles a token carries are read from the same cache the Lark and MCP
// surfaces use, so a person cannot be an administrator on one surface and not on
// another. It is the live cache rather than a snapshot taken here: a token minted
// after a role change reflects it, which is what makes the table the source of
// truth instead of a second copy of a decision made at start-up.
func ProvideAuthBiz(
	cfg *Config, roles roleauthz.Resolver, st store.IStore, oauth *larkoauth.Client,
) authn.AuthBiz {
	providers := authn.ProviderOptions{
		AllowLocal:        cfg.ChatOptions.Login.AllowLocal,
		AllowRegistration: cfg.ChatOptions.Login.AllowRegistration,
		BootstrapAdmin:    cfg.ChatOptions.Login.BootstrapAdmin,
		AllowLark:         cfg.ChatOptions.Login.AllowLark,
		AllowAPIKey:       cfg.ChatOptions.Login.AllowAPIKey,
		AllowLarkOAuth:    oauth.Enabled(),
		TokenTTL:          cfg.ChatOptions.Login.TokenTTL,
	}
	return authn.New(cfg.JWTSecret, providers, roles, st.Users(), oauth, cfg.OIDCSigner)
}

// ProvideLarkOAuth builds the browser-facing Feishu OAuth client.
//
// It is disabled when any of the three configured values is missing, which is
// what lets the console hide the "飞书登录" button on a deployment that never
// registered a callback URL.
func ProvideLarkOAuth(cfg *Config) *larkoauth.Client {
	l := cfg.LarkOptions
	if l == nil {
		return larkoauth.New(larkoauth.Config{})
	}
	return larkoauth.New(larkoauth.Config{
		AppID:       l.AppID,
		AppSecret:   l.AppSecret,
		OpenBase:    l.URL,
		RedirectURI: l.OAuthRedirectURI,
	})
}

// larkProfileResolver adapts larkkit's contact lookup to the narrow interface
// the user biz depends on.
type larkProfileResolver struct {
	resolver *larkkit.ProfileResolver
}

func (r larkProfileResolver) Profile(
	ctx context.Context, openID string,
) (model.LarkProfile, error) {
	profile, err := r.resolver.Profile(ctx, openID)
	return model.LarkProfile{
		LarkUserID: profile.UserID,
		UnionID:    profile.UnionID,
		Name:       profile.Name,
		AvatarURL:  profile.AvatarURL,
	}, err
}

// ProvideLarkProfileResolver builds the open_id -> contact profile lookup.
//
// A deployment without Lark credentials gets nil: both the bind enrichment and
// the backfill are then disabled rather than logging a failure per call.
func ProvideLarkProfileResolver(cfg *Config, larkClient lark.Interface) userbiz.LarkProfileResolver {
	if cfg.LarkOptions == nil ||
		cfg.LarkOptions.AppID == "" || cfg.LarkOptions.AppSecret == "" ||
		larkClient == nil || larkClient.C() == nil {
		return nil
	}
	return larkProfileResolver{resolver: larkkit.NewProfileResolver(larkClient.C(), 0)}
}

// ProvideUserBiz creates the account-administration business logic instance.
func ProvideUserBiz(
	ctx context.Context, cfg *Config, st store.IStore, chat chatbiz.ChatBiz,
	profiles userbiz.LarkProfileResolver,
) userbiz.UserBiz {
	bindTTL := userbiz.DefaultBindTTL
	if claimTTL := cfg.ChatOptions.PeerBind.ClaimTTL; claimTTL > 0 && claimTTL < bindTTL {
		// A console code that outlives the bot's claim would keep telling the
		// user to wait for an answer that can no longer arrive.
		bindTTL = claimTTL
	}
	appID := ""
	if cfg.LarkOptions != nil {
		appID = cfg.LarkOptions.AppID
	}
	// The catalog is the same one the bot resolves @peer against: a name the
	// console accepts must be a name the bot can actually run.
	biz := userbiz.NewWithCatalog(st, appID, bindTTL, chat).WithProfileResolver(profiles)

	// Bindings written before the confirm path resolved the contact record
	// have an open_id but no name/user_id. Repair them once per start; the
	// lookup is rate-limited and every failure is only logged.
	if profiles != nil && st != nil {
		runtime.GoSafe(ctx, "chat.lark_profile.backfill", func() {
			userbiz.RunLarkProfileBackfill(
				ctx, st.Users(), profiles, userbiz.DefaultProfileBackfillDelay)
		})
	}
	return biz
}

// ProvideOncallHistory adapts the chat biz to the one method the on-call read
// surface needs.
//
// It is a provider rather than a wire.Bind because a bind has to sit in the
// same set as the concrete type's provider, and that provider lives in the chat
// package's own set — which must not have to know that this consumer exists.
// The adapter keeps the dependency pointing one way.
func ProvideOncallHistory(b chatbiz.ChatBiz) oncall.History { return b }

// ProvideHandler creates the HTTP handler.
func ProvideHandler(
	b biz.IBiz, hub *wshub.Hub, cfg *Config, val *validation.Validator,
) *handler.Handler {
	return handler.NewHandler(
		b, hub, cfg.JWTSecret, cfg.AnonymousAuth, cfg.AuthorizationMode, cfg.LarkOptions,
		cfg.ChatOptions.InternalCredentials,
		val,
	)
}

// ProvideServerConfig assembles the server configuration from its constituent parts.
func ProvideServerConfig(
	cfg *Config, h *handler.Handler, b biz.IBiz, sweeper dissolve.Sweeper,
	ready *readiness.Registry,
) *ServerConfig {
	return &ServerConfig{
		Config:    cfg,
		Handler:   h,
		Biz:       b,
		Dissolve:  sweeper,
		Readiness: ready,
	}
}

// NewWebServer creates and returns a new web server instance using the provided server configuration.
func NewWebServer(serverConfig *ServerConfig) (server.Server, error) {
	return serverConfig.NewGinServer()
}

// ProvideDB provides a database instance based on the configuration.
func ProvideDB(cfg *Config) (*gorm.DB, error) {
	return cfg.NewDB()
}

// ProvideReadiness answers /readyz. Only post-boot failure modes belong here: a
// database that is missing at start-up already stopped the process.
//
// The database is the one dependency whose loss this instance can survive and
// should not serve through: every conversation, ticket and roster read goes to
// it, so an unreachable database turns each request into a timeout. Leaving the
// load balancer is the right answer; restarting the pod, which is what a
// failing /healthz would do, is not.
func ProvideReadiness(db *gorm.DB) *readiness.Registry {
	return readiness.New(readiness.Check{
		Name: "database",
		Probe: func(ctx context.Context) error {
			sqlDB, err := db.DB()
			if err != nil {
				return err
			}

			return sqlDB.PingContext(ctx)
		},
	})
}
