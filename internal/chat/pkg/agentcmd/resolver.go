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

// Package agentcmd answers which commands a conversation's agents have.
//
// The command tree is rebuilt for every message, so nothing on that path may
// reach a database or another service: the rules come from the rule store's own
// snapshot, the agents they select on come from the control plane's cache (which
// a watch keeps current), and a resolve is matching over those two plus a union
// of sets computed when the rule snapshot was built.
//
// The default is deny. A catalog command comes from the stored rules and from
// nowhere else, so a conversation no rule covers runs none — as does one whose
// agents cannot be named, and one served by a deployment that has never managed
// to read its rules. That is the whole point of the package: the rules are the
// grant list, and a grant list that nobody wrote, or that cannot be read,
// grants nothing.
//
// User-defined commands are the one exception, and it is deliberate: an agent's
// AgentBinding is the whole grant, so a conversation sees the custom commands
// its agents bind even when no rule covers it. The two therefore travel
// different paths below, and "binding is authorization" is a property of the
// model rather than an accident of this code.
package agentcmd

import (
	"context"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/pkg/known/chat"
)

// RuleStore reads the stored rules.
type RuleStore interface {
	// ListEnabled returns every enabled rule.
	ListEnabled(ctx context.Context) ([]*model.AgentCommandM, error)
}

// SkillSource enumerates the skills an agent has exposed as commands.
//
// It is the one part of a resolve that reaches another service, so its answer
// is cached per agent and refreshed off the request path.
type SkillSource interface {
	SkillsOf(ctx context.Context, agent string) ([]runtime.SkillCommand, error)
}

// Options configures a Resolver.
type Options struct {
	// Rules is required; a nil store grants nothing, which is the same answer a
	// deployment with no AgentBinding gives. The store is the AgentBinding
	// projection: a binding is the only way a command is granted.
	Rules RuleStore
	// Agents is required for selector rules. Without it those rules match
	// nobody, and the rules that name agents outright still work.
	Agents AgentLister
	// Skills may be nil, in which case no skill command is ever granted.
	Skills SkillSource
	// Customs may be nil, in which case no user-defined command is ever
	// granted. Unlike skills, these need no rule: an agent's binding is the
	// whole grant.
	Customs runtime.CustomCommandSource
	// TTLs override the defaults when non-zero. Tests set them.
	//
	// There is no agent TTL: the agents come from the control plane's cache,
	// which a watch keeps current, so a second cache on top could only be older.
	RulesTTL   time.Duration
	SkillsTTL  time.Duration
	CustomsTTL time.Duration
	// Now, when set, supplies the clock. Tests set it.
	Now func() time.Time
}

// Resolver answers the AgentCommandResolver question from stored rules.
type Resolver struct {
	opts Options

	rulesSnap atomic.Pointer[rulesSnapshot]
	rulesSF   singleflight.Group

	skills  *ttlCache[string, []runtime.SkillCommand]
	customs *ttlCache[string, []runtime.CustomCommand]
}

// rulesSnapshot is the compiled rules at one moment.
type rulesSnapshot struct {
	at    time.Time
	rules []compiledRule
}

var _ runtime.AgentCommandResolver = (*Resolver)(nil)

// New builds a resolver.
//
// A nil rule store yields a resolver that grants nothing, which is the same
// answer an empty rule table gives: the caller that has no rules to read has
// nothing it can hand out. It is a deployment with the feature unwired, not one
// with the feature off — there is no "off".
func New(opts Options) *Resolver {
	if opts.RulesTTL <= 0 {
		opts.RulesTTL = chat.DefaultRulesTTL
	}
	if opts.SkillsTTL <= 0 {
		opts.SkillsTTL = chat.DefaultSkillsTTL
	}
	if opts.CustomsTTL <= 0 {
		opts.CustomsTTL = chat.DefaultCustomsTTL
	}
	r := &Resolver{opts: opts}
	r.skills = newTTLCache(ttlCacheOptions[string, []runtime.SkillCommand]{
		TTL: opts.SkillsTTL,
		Now: r.now,
		Load: func(ctx context.Context, agent string) ([]runtime.SkillCommand, error) {
			return opts.Skills.SkillsOf(ctx, agent)
		},
		Cold: func(ctx context.Context, agent string, err error) {
			slog.ErrorContext(ctx, "agent commands: cannot list an agent's skills",
				"agent", agent, "error", err)
			metrics.M.RecordAgentCommandDegraded(ctx, "skills")
		},
		Refresh: func(ctx context.Context, agent string, err error) {
			slog.WarnContext(ctx, "agent commands: cannot refresh an agent's skills; it keeps the ones it had",
				"agent", agent, "error", err)
		},
		Op: "agentcmd.refresh_skills",
	})
	r.customs = newTTLCache(ttlCacheOptions[string, []runtime.CustomCommand]{
		TTL: opts.CustomsTTL,
		Now: r.now,
		Load: func(ctx context.Context, agent string) ([]runtime.CustomCommand, error) {
			return opts.Customs.CustomCommandsOf(ctx, agent)
		},
		Cold: func(ctx context.Context, agent string, err error) {
			slog.ErrorContext(ctx, "agent commands: cannot list an agent's custom commands",
				"agent", agent, "error", err)
			metrics.M.RecordAgentCommandDegraded(ctx, "customs")
		},
		Refresh: func(ctx context.Context, agent string, err error) {
			slog.WarnContext(ctx, "agent commands: cannot refresh an agent's custom commands; it keeps the ones it had",
				"agent", agent, "error", err)
		},
		Op: "agentcmd.refresh_customs",
	})
	return r
}

// Invalidate drops the rule snapshot, so the next resolve rereads it. The
// admin write path calls it: a rule that was just saved must not wait out a TTL
// on the replica that saved it.
func (r *Resolver) Invalidate() {
	r.rulesSnap.Store(nil)
}

// Warm loads the per-agent sources before a message needs them.
//
// A cold cache is paid by whoever asks first, and they pay it in full: the
// custom-command list and the skill list are both reads over the wire, and the
// reply waits for them. That used to be the first message after every restart,
// which is exactly the message that looks broken. Warming moves the cost to
// start-up, where nothing is waiting.
//
// It warms the two sources the way a resolve reads them, and no wider: customs
// are unconditional, because every resolve collects them; skills are asked for
// only when a covering rule wants them, so an agent no skill rule covers costs
// nothing. Both are serial, which bounds the burst this can put on the agent
// service at start-up to one request at a time.
func (r *Resolver) Warm(ctx context.Context, agents []string) {
	if r == nil || len(agents) == 0 {
		return
	}

	now := r.now()
	rules := r.rules(ctx, now)
	if len(rules) == 0 {
		return
	}
	snap := r.agentSnapshot(ctx)

	for _, agent := range agents {
		if agent == "" {
			continue
		}
		r.customsOfAgent(ctx, agent, now)
		for _, rule := range rules {
			if !rule.wantsSkills() || !rule.covers([]string{agent}, snap) {
				continue
			}
			r.skillsOf(ctx, agent, now)
			break
		}
	}
}

// Resolve implements runtime.AgentCommandResolver.
//
// Every path out of here but one ends in the empty selection, and the one that
// does not is the only one that read a rule that covers the conversation. There
// is no branch that hands out a command because nobody said otherwise.
func (r *Resolver) Resolve(ctx context.Context, agents []string) runtime.CommandSelection {
	now := r.now()
	if len(agents) == 0 {
		// No agent context: a surface without a conversation, or one whose
		// agents have not been resolved yet. Nobody can have granted commands
		// to an agent that cannot be named, so there is nothing to hand out.
		return runtime.CommandSelection{ResolvedAt: now}
	}

	// Binding is the whole grant for a user-defined command, so this read does
	// not wait for a rule: a conversation with no rules at all still sees the
	// commands its agents bind.
	customs := r.customsOf(ctx, agents, now)

	rules := r.rules(ctx, now)
	if len(rules) == 0 {
		// A deployment whose rule table is empty — the common case of one that
		// has not been configured yet, and the one that follows a table someone
		// emptied by accident. Neither is a reason to hand out every command,
		// so the agent list is never read and nobody is granted anything.
		metrics.M.RecordAgentCommandNoGrant(ctx, metrics.NoGrantNoRules)
		return runtime.CommandSelection{Customs: customs, ResolvedAt: now}
	}

	snap := r.agentSnapshot(ctx)
	granted := r.accumulate(ctx, agents, now, rules, snap)

	if len(granted.covering) == 0 {
		// No rule covers this conversation, so it has no catalog commands. That
		// is the default rather than a misconfiguration — but it is also what a
		// typo in a selector looks like, and the two are indistinguishable from
		// the outside: both are a silent menu. The counter is how an operator
		// sees how many conversations are in this state.
		metrics.M.RecordAgentCommandNoGrant(ctx, metrics.NoGrantUncovered)
		return runtime.CommandSelection{Customs: customs, ResolvedAt: now}
	}

	selection := runtime.CommandSelection{
		Names:      granted.names,
		Skills:     sortSkills(granted.skills),
		Customs:    customs,
		ResolvedAt: now,
	}
	if !selection.AllowsAny() {
		// A rule covers this conversation and gives it nothing. Unlike the
		// branch above, this is never deliberate — an empty grant is what a
		// typo in a selector looks like — and reporting it is what keeps "the
		// rule did not work" from being indistinguishable from "the rule said
		// nothing".
		slog.ErrorContext(ctx, "agent commands: a rule covers this conversation but grants nothing",
			"agents", agents, "rules", len(rules))
		metrics.M.RecordAgentCommandEmptyGrant(ctx)
	}
	return selection
}

// granted is what one pass over the rules accumulated for a conversation.
//
// The covering rules are carried beside the commands because the two answers an
// operator has to tell apart are "nobody wrote a rule for this" and "a rule
// covers it and grants nothing" — and both of them are the empty command set.
type granted struct {
	names  map[string]struct{}
	skills map[string]runtime.SkillCommand
	// covering names the rules that matched, in the order they were read.
	covering []string
}

// accumulate runs every rule over the conversation's agents and collects what
// they grant between them.
//
// It is the half a resolve and the admin report have in common: both need the
// union of the rules that cover a set of agents, and neither may drift from the
// other, or the report would describe a command set nobody is actually handed.
// The two callers differ only in the counters they record, so that is where
// they differ.
func (r *Resolver) accumulate(
	ctx context.Context,
	agents []string,
	now time.Time,
	rules []compiledRule,
	snap *agentSnapshot,
) granted {
	g := granted{
		names:  make(map[string]struct{}),
		skills: make(map[string]runtime.SkillCommand),
	}

	for _, rule := range rules {
		if !rule.covers(agents, snap) {
			continue
		}
		g.covering = append(g.covering, rule.name)

		for name := range rule.commands {
			g.names[name] = struct{}{}
		}
		if !rule.wantsSkills() {
			continue
		}
		for _, agent := range agents {
			for _, sk := range r.skillsOf(ctx, agent, now) {
				if rule.grantsSkill(sk) {
					g.skills[sk.Name] = sk
				}
			}
		}
	}

	return g
}

// AgentCoverage is what the stored rules say about one agent.
type AgentCoverage struct {
	// Agent is the agent's name, as a rule names it.
	Agent string `json:"agent"`
	// Rules are the enabled rules that cover it, in the order they were read.
	// Empty means no rule covers it, and it therefore runs no command at all.
	Rules []string `json:"rules"`
	// Commands are the catalog commands it would be granted, sorted.
	Commands []string `json:"commands"`
	// Skills are the skill commands it would be granted, sorted.
	Skills []string `json:"skills"`
}

// Coverage is the whole fleet under the stored rules.
type Coverage struct {
	// Agents are every agent the agent service listed, in its order. A listing
	// that includes disabled agents means this one does too: the lister carries
	// no status, and a report that quietly dropped records would be answering a
	// narrower question than the one it appears to answer.
	Agents []AgentCoverage `json:"agents"`
	// WithNothing is how many of them no rule grants anything to. It is the
	// number this report exists for: commands are deny-by-default, so an agent
	// nobody wrote a rule for is indistinguishable from a working one until
	// somebody tries to use it.
	WithNothing int `json:"withNothing"`
}

// CoverageOf reports what the enabled rules grant every agent the service
// knows, and which rules did it.
//
// It is the agent-centric half of the same question Preview answers: a preview
// says what one rule does to the fleet, and this says what the fleet ends up
// with. Neither can be answered from the other — a fleet report has to union
// every rule, and the rule that is missing is invisible in a list of the ones
// that are there. Reading it is how an operator sees the blast radius of a
// deny-by-default rule table before a conversation hits it.
//
// The agents are read fresh rather than from the snapshot, for the reason
// Preview's read is: the answer is wanted in the minute after somebody changed
// an agent's tags, and an answer served from the snapshot taken before that
// change is one nobody can tell from the right answer.
//
// It costs what the agents in it cost: a covered agent whose rule can reach a
// skill has its skill list read, exactly as a conversation with it would. The
// list is cached per agent, so a second report inside the skills TTL is free,
// but a deployment with many covered agents should expect its skill source to
// be asked once per agent per TTL — the price of a report that cannot call a
// working agent dark.
func (r *Resolver) CoverageOf(ctx context.Context) (*Coverage, error) {
	now := r.now()

	snap, err := r.agentSnapshotRead(ctx)
	if err != nil {
		// An empty report would read as "there are no agents", which is the one
		// thing this report must not say when it does not know.
		return nil, err
	}

	rules := r.rules(ctx, now)

	coverage := &Coverage{Agents: make([]AgentCoverage, 0, len(snap.names))}
	for _, agent := range snap.names {
		granted := r.accumulate(ctx, []string{agent}, now, rules, snap)

		entry := AgentCoverage{
			Agent:    agent,
			Rules:    emptyIfNil(granted.covering),
			Commands: emptyIfNil(sortNames(granted.names)),
			Skills:   emptyIfNil(skillNames(granted.skills)),
		}
		if len(entry.Commands) == 0 && len(entry.Skills) == 0 {
			coverage.WithNothing++
		}
		coverage.Agents = append(coverage.Agents, entry)
	}

	return coverage, nil
}

// now reads the clock.
func (r *Resolver) now() time.Time {
	if r.opts.Now != nil {
		return r.opts.Now()
	}
	return time.Now()
}

// rules returns the compiled rules, refreshing them in the background when they
// have gone stale.
func (r *Resolver) rules(ctx context.Context, now time.Time) []compiledRule {
	cur := r.rulesSnap.Load()
	if cur != nil && now.Sub(cur.at) < r.opts.RulesTTL {
		return cur.rules
	}

	if cur == nil {
		// Nothing to serve, so this one request waits for the read rather than
		// answering from a snapshot that does not exist.
		loaded, err := r.loadRules(ctx, now)
		if err != nil {
			slog.ErrorContext(ctx, "agent commands: cannot read the rules and have none to serve; nothing is granted", "error", err)
			metrics.M.RecordAgentCommandDegraded(ctx, "rules")
			return nil
		}
		return loaded.rules
	}

	// Stale: answer from what is already loaded and refresh off this request's
	// context, so a slow store costs the next message rather than this one.
	bg := context.WithoutCancel(ctx)
	runtime.GoSafe(bg, "agentcmd.refresh_rules", func() {
		if _, err := r.loadRules(bg, r.now()); err != nil {
			slog.WarnContext(bg, "agent commands: cannot refresh the rules; serving the previous ones", "error", err)
		}
	})
	return cur.rules
}

// loadRules reads and compiles the rules, collapsing concurrent rebuilds.
//
// A failed read still publishes a snapshot: it keeps the rules that were
// already loaded and moves the timestamp forward, so a store that is down is
// retried once per TTL instead of once per message.
func (r *Resolver) loadRules(ctx context.Context, now time.Time) (*rulesSnapshot, error) {
	v, err, _ := r.rulesSF.Do("rules", func() (any, error) {
		// Another goroutine may have refreshed while this one waited.
		if cur := r.rulesSnap.Load(); cur != nil && now.Sub(cur.at) < r.opts.RulesTTL {
			return cur, nil
		}

		models, err := r.opts.Rules.ListEnabled(ctx)
		if err != nil {
			kept := []compiledRule(nil)
			if cur := r.rulesSnap.Load(); cur != nil {
				kept = cur.rules
			}
			r.rulesSnap.Store(&rulesSnapshot{at: now, rules: kept})
			return nil, err
		}

		snap := &rulesSnapshot{at: now, rules: compileRules(models)}
		r.rulesSnap.Store(snap)
		return snap, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*rulesSnapshot), nil
}

// compileRules compiles every rule, dropping the ones whose selectors do not
// parse.
//
// Dropping rather than failing is the read-path rule this service follows
// everywhere: one unreadable row — written by a migration, or by an operator at
// a SQL prompt — must not cost every agent their command set. The write path
// refuses the same value while the operator is still there to be told.
//
// A rule that names a command the catalog does not have is kept, because the
// rest of it still works, and reported here — that is the only place a rename
// shows up. The name is dropped from what the rule grants, so nothing fails:
// the rule simply covers fewer commands than it says, and the person who
// notices is a user who cannot run one. The warning repeats with every refresh
// that recompiles the rules, on purpose and like the parse warning above: a
// rule that has drifted from the catalog stays drifted until somebody edits it.
func compileRules(models []*model.AgentCommandM) []compiledRule {
	rules := make([]compiledRule, 0, len(models))
	for _, m := range models {
		rule, err := compileRule(m)
		if err != nil {
			slog.Warn("agent commands: ignoring a rule whose selector does not parse", "rule", m.Name, "error", err)
			continue
		}
		if len(rule.orphans) > 0 {
			slog.Warn("agent commands: a rule names commands the catalog does not have; they grant nothing",
				"rule", m.Name, "commands", rule.orphans)
		}
		rules = append(rules, rule)
	}
	return rules
}

// agentSnapshotRead reads the agents' labels and columns from the control
// plane's cache.
//
// It is the only agent read there is. The lister behind it is maintained by a
// watch, so a copy on top could only be older — and the TTL it replaced was a
// holdover from reading this over HTTP. A failed read is an error; each caller
// decides what to do with it.
func (r *Resolver) agentSnapshotRead(ctx context.Context) (*agentSnapshot, error) {
	if r.opts.Agents == nil {
		return emptyAgentSnapshot(), nil
	}
	infos, err := r.opts.Agents.ListAgentInfo(ctx)
	if err != nil {
		return nil, err
	}
	return buildAgentSnapshot(infos), nil
}

// agentSnapshot is the message path's read: an unreadable agent list is an
// empty one, so every selector rule matches nobody. That is the same
// fail-closed answer the command surface gives for an unreadable rule set, and
// it is the only answer that cannot grant something by accident.
func (r *Resolver) agentSnapshot(ctx context.Context) *agentSnapshot {
	snap, err := r.agentSnapshotRead(ctx)
	if err == nil {
		return snap
	}
	slog.ErrorContext(ctx, "agent commands: cannot read the agents; label rules match nobody", "error", err)
	metrics.M.RecordAgentCommandDegraded(ctx, "agents")
	return emptyAgentSnapshot()
}

// emptyAgentSnapshot is the label view of a deployment whose agents could not
// be read: no labels, so no selector rule matches.
func emptyAgentSnapshot() *agentSnapshot {
	return &agentSnapshot{set: map[string]labels.Set{}, fields: map[string]fields.Set{}}
}

// skillsOf returns an agent's skill commands, refreshing them in the background
// when they have gone stale.
func (r *Resolver) skillsOf(ctx context.Context, agent string, now time.Time) []runtime.SkillCommand {
	if r.opts.Skills == nil {
		return nil
	}
	return r.skills.get(ctx, agent, now)
}

// sortSkills orders the granted skills by name, so a menu built from them does
// not reorder itself between two messages that resolved the same set.
func sortSkills(skills map[string]runtime.SkillCommand) []runtime.SkillCommand {
	if len(skills) == 0 {
		return nil
	}
	out := make([]runtime.SkillCommand, 0, len(skills))
	for _, sk := range skills {
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// customsOf returns the union of the user-defined commands the agents bind,
// refreshing each agent's list in the background when it has gone stale.
func (r *Resolver) customsOf(ctx context.Context, agents []string, now time.Time) []runtime.CustomCommand {
	if r.opts.Customs == nil || len(agents) == 0 {
		return nil
	}
	byName := make(map[string]runtime.CustomCommand)
	for _, agent := range agents {
		for _, command := range r.customsOfAgent(ctx, agent, now) {
			if _, taken := byName[command.Name]; taken {
				continue
			}
			command.Agent = agent
			byName[command.Name] = command
		}
	}
	return sortCustoms(byName)
}

func (r *Resolver) customsOfAgent(ctx context.Context, agent string, now time.Time) []runtime.CustomCommand {
	return r.customs.get(ctx, agent, now)
}

// sortCustoms orders the commands by name, so a menu does not reorder itself
// between two messages that resolved the same set.
func sortCustoms(byName map[string]runtime.CustomCommand) []runtime.CustomCommand {
	if len(byName) == 0 {
		return nil
	}
	out := make([]runtime.CustomCommand, 0, len(byName))
	for _, command := range byName {
		out = append(out, command)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// sortNames orders a granted command set by name, for the same reason sortSkills
// orders a skill set: a report that reorders itself between two reads of the
// same rules cannot be diffed against the last one.
func sortNames(names map[string]struct{}) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// emptyIfNil turns an absent list into an empty one, for the report.
//
// The two are the same Go value to every caller that only measures them, and
// different JSON — "skills": [] against "skills": null — to the operator who
// reads the report looking for the entries with nothing in them.
func emptyIfNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// skillNames is sortSkills reduced to the names, for the report that needs the
// grant rather than the command.
func skillNames(skills map[string]runtime.SkillCommand) []string {
	sorted := sortSkills(skills)
	if len(sorted) == 0 {
		return nil
	}
	out := make([]string, 0, len(sorted))
	for _, sk := range sorted {
		out = append(out, sk.Name)
	}
	return out
}
