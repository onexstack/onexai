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

package agentcmd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/pkg/known/chat"
)

// fakeRules is a rule store that counts its reads, so a test can tell a cached
// resolve from one that reached the database.
type fakeRules struct {
	mu    sync.Mutex
	calls int
	rules []*model.AgentCommandM
	err   error
}

func (f *fakeRules) ListEnabled(context.Context) ([]*model.AgentCommandM, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.rules, nil
}

func (f *fakeRules) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeAgents is an agent lister over a fixed set.
type fakeAgents struct {
	mu     sync.Mutex
	calls  int
	agents []AgentInfo
	err    error
}

func (f *fakeAgents) ListAgentInfo(context.Context) ([]AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.agents, nil
}

func (f *fakeAgents) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeSkills is a skill source over a fixed per-agent map.
type fakeSkills struct {
	skills map[string][]runtime.SkillCommand
	err    error
}

func (f *fakeSkills) SkillsOf(_ context.Context, agent string) ([]runtime.SkillCommand, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.skills[agent], nil
}

// rule builds a stored rule with the given columns.
func rule(name, agentSelector, agents, commandSelector, commands string) *model.AgentCommandM {
	return &model.AgentCommandM{
		Name:            name,
		Status:          model.AgentCommandStatusEnabled,
		AgentSelector:   agentSelector,
		Agents:          agents,
		CommandSelector: commandSelector,
		Commands:        commands,
	}
}

// groupSelector builds the command selector that grants a set of groups, the
// way an operator writes it.
func groupSelector(groups ...string) string {
	joined := ""
	for i, g := range groups {
		if i > 0 {
			joined += ","
		}
		joined += command.LabelGroup + "=" + g
	}
	return joined
}

// newResolver builds a resolver over the given fakes and a test clock.
//
// skills is taken as the interface rather than as *fakeSkills so that a caller
// with no skill source passes a nil interface: a typed nil would be non-nil
// inside and the resolver would call it.
func newResolver(rules *fakeRules, agents *fakeAgents, skills SkillSource) *Resolver {
	return New(Options{
		Rules:  rules,
		Agents: agents,
		Skills: skills,
		Now:    func() time.Time { return time.Unix(0, 0) },
	})
}

// TestResolve_NoAgentContextGrantsNothing pins the answer a surface without a
// conversation gets. There is no agent here, so there is no agent a rule could
// have covered and nothing to hand out — a grant is a statement about an agent,
// and this request cannot say which.
func TestResolve_NoAgentContextGrantsNothing(t *testing.T) {
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("r", "", `["*"]`, groupSelector(command.GroupCore), ""),
	}}
	r := newResolver(rules, &fakeAgents{}, nil)

	got := r.Resolve(context.Background(), nil)

	if len(got.Names) != 0 || len(got.Skills) != 0 {
		t.Errorf("a caller with no agent context was granted %+v", got)
	}
	if got.Allows("doctor") {
		t.Errorf("doctor was granted to a caller with no agent context")
	}
	if rules.reads() != 0 {
		t.Errorf("the rule store was read %d times; a caller with no agent cannot match anything", rules.reads())
	}
}

// TestResolve_NoRulesGrantsNothing pins the deployment whose rule table is
// empty — the one nobody has configured yet, and the one that follows somebody
// emptying the table. Neither is a reason to hand out every command: the rules
// are the grant list, and an empty grant list grants nothing.
func TestResolve_NoRulesGrantsNothing(t *testing.T) {
	agents := &fakeAgents{agents: []AgentInfo{{Name: "bke-agent"}}}
	r := newResolver(&fakeRules{}, agents, nil)

	got := r.Resolve(context.Background(), []string{"bke-agent"})

	if len(got.Names) != 0 || len(got.Skills) != 0 {
		t.Errorf("an agent was granted commands with no rules stored: %+v", got)
	}
	if got.Allows("doctor") {
		t.Errorf("doctor was granted with no rules stored")
	}
	if agents.reads() != 0 {
		t.Errorf("the agent service was read %d times with no rules to match", agents.reads())
	}
}

// TestResolve covers the matching itself: which agents a rule covers, and what
// it hands them.
func TestResolve(t *testing.T) {
	agents := &fakeAgents{agents: []AgentInfo{
		{Name: "bke-agent", Labels: map[string]string{"sre": ""}},
		{Name: "plain-agent"},
	}}

	tests := []struct {
		name   string
		rules  []*model.AgentCommandM
		asking []string
		// want is the whole of the answer: a case no rule covers wants the empty
		// list, which is what makes the default visible in this table rather
		// than implied by it.
		want []string
	}{
		{
			name:   "a rule naming an agent grants what it selects",
			rules:  []*model.AgentCommandM{rule("r", "", `["bke-agent"]`, groupSelector(command.GroupCore), "")},
			asking: []string{"bke-agent"},
			want:   []string{"doctor", "bind-onex"},
		},
		{
			name:   "a rule naming another agent does not cover this one",
			rules:  []*model.AgentCommandM{rule("r", "", `["someone-else"]`, groupSelector(command.GroupCore), "")},
			asking: []string{"bke-agent"},
			// The rule is about an agent this conversation does not run, so the
			// conversation has no grant at all — the same answer as a rule table
			// nobody wrote.
			want: []string{},
		},
		{
			name:   "a star covers every agent",
			rules:  []*model.AgentCommandM{rule("r", "", `["*"]`, groupSelector(command.GroupCore), "")},
			asking: []string{"plain-agent"},
			want:   []string{"doctor", "bind-onex"},
		},
		{
			name:   "a selector covers by tag, with or without the prefix",
			rules:  []*model.AgentCommandM{rule("r", "sre", "", groupSelector(command.GroupCore), "")},
			asking: []string{"bke-agent"},
			want:   []string{"doctor", "bind-onex"},
		},
		{
			name:   "a selector matches nobody when the tag is absent",
			rules:  []*model.AgentCommandM{rule("r", "sre", "", groupSelector(command.GroupCore), "")},
			asking: []string{"plain-agent"},
			// A label selector fails closed for an agent with no tags, so the
			// rule that was meant to cover the SREs covers nobody here — and a
			// rule covering nobody is not a rule covering everybody.
			want: []string{},
		},
		{
			name:   "an unknown agent is not matched by a selector, only by its name",
			rules:  []*model.AgentCommandM{rule("r", "sre", "", groupSelector(command.GroupCore), "")},
			asking: []string{"never-listed"},
			want:   []string{},
		},
		{
			name: "several rules are a union",
			rules: []*model.AgentCommandM{
				rule("a", "", `["bke-agent"]`, groupSelector(command.GroupCore), ""),
				rule("b", "", `["bke-agent"]`, "", `["pod"]`),
			},
			asking: []string{"bke-agent"},
			want:   []string{"doctor", "bind-onex", "pod"},
		},
		{
			name: "a rule covers a conversation running as several agents",
			rules: []*model.AgentCommandM{
				rule("a", "", `["bke-agent"]`, groupSelector(command.GroupCore), ""),
				rule("b", "", `["plain-agent"]`, "", `["pod"]`),
			},
			asking: []string{"bke-agent", "plain-agent"},
			want:   []string{"doctor", "bind-onex", "pod"},
		},
		{
			name:   "a name outside the catalog grants nothing",
			rules:  []*model.AgentCommandM{rule("r", "", `["bke-agent"]`, "", `["pod","no-such-command"]`)},
			asking: []string{"bke-agent"},
			want:   []string{"pod"},
		},
		{
			name:   "a group the catalog does not have grants nothing",
			rules:  []*model.AgentCommandM{rule("r", "", `["bke-agent"]`, command.LabelGroup+"=no-such-group", "")},
			asking: []string{"bke-agent"},
			// A rule covers the agent and grants nothing: an empty set, not
			// "everything". Falling back to everything here would turn a typo
			// into a grant, which is the opposite of what was asked for.
			want: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newResolver(&fakeRules{rules: tc.rules}, agents, nil)

			got := r.Resolve(context.Background(), tc.asking)

			if len(got.Names) != len(tc.want) {
				t.Fatalf("Names = %v, want %v", got.Names, tc.want)
			}
			for _, name := range tc.want {
				if !got.Allows(name) {
					t.Errorf("Names is missing %q: %v", name, got.Names)
				}
			}
		})
	}
}

// TestResolve_Skills covers the skill dimension: a skill is not in the catalog,
// so a rule reaches it by name or by the labels the skill itself carries.
func TestResolve_Skills(t *testing.T) {
	agents := &fakeAgents{agents: []AgentInfo{{Name: "bke-agent"}}}
	skills := &fakeSkills{skills: map[string][]runtime.SkillCommand{
		"bke-agent": {
			{Name: "k8s-troubleshoot", Provider: "github"},
			{Name: "weekly-report", Provider: "lark_doc"},
		},
	}}

	tests := []struct {
		name  string
		rules []*model.AgentCommandM
		want  []string
	}{
		{
			name:  "a skill named outright is granted",
			rules: []*model.AgentCommandM{rule("r", "", `["bke-agent"]`, "", `["skill:k8s-troubleshoot"]`)},
			want:  []string{"k8s-troubleshoot"},
		},
		{
			name:  "the skill group grants every skill the agent has",
			rules: []*model.AgentCommandM{rule("r", "", `["bke-agent"]`, groupSelector(command.GroupSkill), "")},
			want:  []string{"k8s-troubleshoot", "weekly-report"},
		},
		{
			name:  "a provider grants every skill from that repository kind",
			rules: []*model.AgentCommandM{rule("r", "", `["bke-agent"]`, SkillProviderLabel+"=lark_doc", "")},
			want:  []string{"weekly-report"},
		},
		{
			name:  "a skill the agent does not have is not granted",
			rules: []*model.AgentCommandM{rule("r", "", `["bke-agent"]`, "", `["skill:no-such-skill"]`)},
			want:  []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newResolver(&fakeRules{rules: tc.rules}, agents, skills)

			got := r.Resolve(context.Background(), []string{"bke-agent"})

			if len(got.Skills) != len(tc.want) {
				t.Fatalf("Skills = %+v, want %v", got.Skills, tc.want)
			}
			for i, name := range tc.want {
				if got.Skills[i].Name != name {
					t.Errorf("Skills[%d] = %q, want %q", i, got.Skills[i].Name, name)
				}
			}
		})
	}
}

// TestResolve_CachesTheRulesOnTheHotPath pins the property the rule snapshot
// exists for: the command tree is rebuilt for every message, so a resolve must
// not reach the database.
//
// The agents are not cached here because they are already in one: they come from
// the control plane's lister, an in-memory read a watch keeps current, so a TTL
// on top of it could only be older than what is in it.
func TestResolve_CachesTheRulesOnTheHotPath(t *testing.T) {
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("r", "sre", "", groupSelector(command.GroupCore), ""),
	}}
	agents := &fakeAgents{agents: []AgentInfo{{Name: "bke-agent", Labels: map[string]string{"sre": ""}}}}
	r := newResolver(rules, agents, nil)

	for i := 0; i < 20; i++ {
		r.Resolve(context.Background(), []string{"bke-agent"})
	}

	if rules.reads() != 1 {
		t.Errorf("the rule store was read %d times, want 1", rules.reads())
	}
	if got := agents.reads(); got != 20 {
		t.Errorf("the agents were read %d times, want one per resolve (20): the lister is the cache", got)
	}
}

// TestResolve_InvalidateRereadsTheRules pins the write path's escape from the
// TTL: the replica that saved a rule must not make its author wait to see it.
func TestResolve_InvalidateRereadsTheRules(t *testing.T) {
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("r", "", `["bke-agent"]`, groupSelector(command.GroupCore), ""),
	}}
	r := newResolver(rules, &fakeAgents{}, nil)

	if got := r.Resolve(context.Background(), []string{"bke-agent"}); !got.Allows("doctor") {
		t.Fatalf("the first resolve did not see the rule: %+v", got)
	}

	rules.mu.Lock()
	rules.rules = nil
	rules.mu.Unlock()
	r.Invalidate()

	if got := r.Resolve(context.Background(), []string{"bke-agent"}); got.Allows("doctor") {
		t.Errorf("the rule was still applied after Invalidate: %+v", got)
	}
}

// TestResolve_UnreadableRulesGrantNothing pins the failure direction for a
// resolver that has never managed to read its rules: there is nothing to serve,
// and an unreadable grant list is an unknown grant list, which grants nothing.
//
// It also must not be retried per message: a failing store that is asked on
// every message turns a hiccup into an outage.
func TestResolve_UnreadableRulesGrantNothing(t *testing.T) {
	rules := &fakeRules{err: errors.New("the database is down")}
	r := newResolver(rules, &fakeAgents{}, nil)

	for i := 0; i < 10; i++ {
		if got := r.Resolve(context.Background(), []string{"bke-agent"}); got.Allows("doctor") {
			t.Fatalf("resolve %d granted a command while the rules were unreadable: %+v", i, got)
		}
	}

	if rules.reads() != 1 {
		t.Errorf("the rule store was read %d times, want 1: a failing store must be retried once per TTL", rules.reads())
	}
}

// TestResolve_UnreadableRulesKeepServingTheLastOnesRead is the other half of the
// failure direction, and the one that keeps a database hiccup from emptying
// every menu: once the rules have been read, a store that stops answering leaves
// the last answer in place rather than replacing it with "nothing is granted".
//
// It is the reason the failure path stores a snapshot carrying the kept rules
// and a fresh timestamp: the next message is served from those rules, and the
// store is not asked again until the TTL is up.
func TestResolve_UnreadableRulesKeepServingTheLastOnesRead(t *testing.T) {
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("r", "", `["bke-agent"]`, groupSelector(command.GroupCore), ""),
	}}
	// A clock the test moves, rather than newResolver's fixed one: the point is
	// to cross the TTL, and an unmoving clock would never refresh at all.
	clock := time.Unix(0, 0)
	r := New(Options{
		Rules:  rules,
		Agents: &fakeAgents{},
		Now:    func() time.Time { return clock },
	})

	if got := r.Resolve(context.Background(), []string{"bke-agent"}); !got.Allows("doctor") {
		t.Fatalf("the first resolve did not see the rule: %+v", got)
	}

	// The store goes down and the rules go stale: a refresh fails, and what is
	// already known must survive it. The refresh runs off the request path, so
	// even the request that triggers it answers from the loaded snapshot.
	rules.mu.Lock()
	rules.rules = nil
	rules.err = errors.New("the database is down")
	rules.mu.Unlock()
	clock = clock.Add(2 * chat.DefaultRulesTTL)

	for i := 0; i < 3; i++ {
		got := r.Resolve(context.Background(), []string{"bke-agent"})
		if !got.Allows("doctor") {
			t.Fatalf("resolve %d dropped the rules that had already been read: %+v", i, got)
		}
	}

	// The refresh runs off the request path, so the assertion above would hold
	// even if it never ran. Wait for the store to have been asked a second time
	// and failed: that is what makes this a test of the failure path rather than
	// of a cache that was never invalidated.
	deadline := time.Now().Add(2 * time.Second)
	for rules.reads() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if rules.reads() < 2 {
		t.Fatalf("the store was read %d times, want 2: the stale snapshot did not trigger a refresh", rules.reads())
	}
}

// TestResolve_UnreadableAgentsFailClosed pins the other direction: the rules
// were read, and an agent that cannot be labelled cannot be matched by a
// selector. It must not match by accident — a selector rule covers nobody.
func TestResolve_UnreadableAgentsFailClosed(t *testing.T) {
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("r", "sre", "", groupSelector(command.GroupCore), ""),
	}}
	agents := &fakeAgents{err: errors.New("the agent service is down")}
	r := newResolver(rules, agents, nil)

	got := r.Resolve(context.Background(), []string{"bke-agent"})

	if got.Allows("doctor") {
		t.Errorf("a selector rule covered an agent whose labels could not be read: %+v", got)
	}
}

// TestResolve_NamedAgentsSurviveAFailedAgentRead pins that the half of the
// matching that needs no labels keeps working when the other half cannot.
func TestResolve_NamedAgentsSurviveAFailedAgentRead(t *testing.T) {
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("r", "", `["bke-agent"]`, groupSelector(command.GroupCore), ""),
	}}
	agents := &fakeAgents{err: errors.New("the agent service is down")}
	r := newResolver(rules, agents, nil)

	got := r.Resolve(context.Background(), []string{"bke-agent"})

	if !got.Allows("doctor") {
		t.Errorf("a rule naming an agent did not grant anything while the agent list was unreadable: %+v", got)
	}
}

// TestResolve_UnparsableSelectorIsIgnored pins the read path's leniency: one
// row nobody can read must not cost every agent its command set. The write path
// refuses the same value, which is where an operator can still be told.
func TestResolve_UnparsableSelectorIsIgnored(t *testing.T) {
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("broken", "!!!not a selector!!!", "", groupSelector(command.GroupCore), ""),
		rule("good", "", `["bke-agent"]`, "", `["pod"]`),
	}}
	r := newResolver(rules, &fakeAgents{}, nil)

	got := r.Resolve(context.Background(), []string{"bke-agent"})

	if !got.Allows("pod") {
		t.Errorf("a broken rule took the readable one down with it: %+v", got)
	}
	if got.Allows("doctor") {
		t.Error("the broken rule's grant was applied")
	}
}

// TestResolve_AFailingSkillSourceIsAskedOncePerTTL pins the negative cache.
//
// The command tree is built twice per invocation — once to assemble it and once
// for the grant gate — so a skill source that is down would be asked, and would
// report, twice per message. One broken dependency must not become a log flood,
// which is the same reason the rule and agent snapshots move their clock
// forward on failure.
func TestResolve_AFailingSkillSourceIsAskedOncePerTTL(t *testing.T) {
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("r", "", `["bke-agent"]`, groupSelector(command.GroupSkill), ""),
	}}
	agents := &fakeAgents{agents: []AgentInfo{{Name: "bke-agent"}}}

	counting := &countingSkills{err: errors.New("the agent service is down")}
	r := newResolver(rules, agents, counting)

	for i := 0; i < 6; i++ {
		r.Resolve(context.Background(), []string{"bke-agent"})
	}

	if counting.calls != 1 {
		t.Errorf("the skill source was asked %d times in six resolves, want 1", counting.calls)
	}
}

// TestResolve_AStaleSkillListSurvivesAFailure pins the other half: an agent's
// skills that were known once are still granted while the source is down. The
// alternative — dropping them — would make a momentary outage look like a
// command that was never there.
func TestResolve_AStaleSkillListSurvivesAFailure(t *testing.T) {
	clock := time.Unix(0, 0)
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("r", "", `["bke-agent"]`, groupSelector(command.GroupSkill), ""),
	}}
	agents := &fakeAgents{agents: []AgentInfo{{Name: "bke-agent"}}}

	source := &countingSkills{skills: map[string][]runtime.SkillCommand{
		"bke-agent": {{Name: "k8s-troubleshoot"}},
	}}
	r := New(Options{Rules: rules, Agents: agents, Skills: source, Now: func() time.Time { return clock }})

	if got := r.Resolve(context.Background(), []string{"bke-agent"}); len(got.Skills) != 1 {
		t.Fatalf("the first resolve granted %+v, want the agent's skill", got.Skills)
	}

	// The source goes down, and the TTL passes.
	source.err = errors.New("the agent service is down")
	clock = clock.Add(3 * time.Minute)

	got := r.Resolve(context.Background(), []string{"bke-agent"})
	if len(got.Skills) != 1 || got.Skills[0].Name != "k8s-troubleshoot" {
		t.Errorf("Skills = %+v after a failed refresh, want the previous list", got.Skills)
	}
}

// countingSkills is a skill source that counts its calls and can be broken.
type countingSkills struct {
	mu     sync.Mutex
	calls  int
	skills map[string][]runtime.SkillCommand
	err    error
}

func (c *countingSkills) SkillsOf(_ context.Context, agent string) ([]runtime.SkillCommand, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.skills[agent], nil
}

// withFields builds a rule whose agent clause is a field selector.
func withFields(name, agentFieldSelector, commandSelector string) *model.AgentCommandM {
	r := rule(name, "", "", commandSelector, "")
	r.AgentFieldSelector = agentFieldSelector
	return r
}

// TestResolve_SelectsAgentsByField covers the second selection space.
//
// A record's own columns are not labels: nobody wrote them as tags, so a rule
// that wants "every agent in this workspace" has to reach them through the
// field selector. It is also the space whose names are a closed set, which is
// what makes a misspelled one refusable — see the write-path test.
func TestResolve_SelectsAgentsByField(t *testing.T) {
	untagged := &fakeAgents{agents: []AgentInfo{
		{Name: "bke-agent", Namespace: "bke"},
		{Name: "plain-agent"},
	}}

	tests := []struct {
		name   string
		agents *fakeAgents
		rule   *model.AgentCommandM
		asking []string
		// want is the command the rule grants, or empty when it covers nobody:
		// a rule that selects no agent in this conversation grants nothing.
		want string
	}{
		{
			name:   "the agent whose record matches is covered",
			agents: untagged,
			rule:   withFields("r", "metadata.workspace=bke", groupSelector(command.GroupCore)),
			asking: []string{"bke-agent"},
			want:   "doctor",
		},
		{
			name:   "an agent whose record does not match is not",
			agents: untagged,
			rule:   withFields("r", "metadata.workspace=bke", groupSelector(command.GroupCore)),
			asking: []string{"plain-agent"},
		},
		{
			name:   "the defaulted workspace is selectable",
			agents: untagged,
			rule:   withFields("r", "metadata.workspace=helper-public", groupSelector(command.GroupCore)),
			asking: []string{"plain-agent"},
			want:   "doctor",
		},
		{
			name:   "a name selector covers the one agent it names",
			agents: untagged,
			rule:   withFields("r", "metadata.name=bke-agent", groupSelector(command.GroupCore)),
			asking: []string{"bke-agent"},
			want:   "doctor",
		},
		{
			name:   "a name selector covers nobody else",
			agents: untagged,
			rule:   withFields("r", "metadata.name=bke-agent", groupSelector(command.GroupCore)),
			asking: []string{"plain-agent"},
		},
		{
			// The point of the union: an agent reached by a field and an agent
			// reached by a label are both "covered", and a rule may use either
			// or both.
			name:   "a label rule and a field rule cover different agents",
			agents: &fakeAgents{agents: []AgentInfo{{Name: "plain-agent", Labels: map[string]string{"sre": ""}}}},
			rule:   rule("r", "sre", "", "", `["pod"]`),
			asking: []string{"plain-agent"},
			want:   "pod",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newResolver(&fakeRules{rules: []*model.AgentCommandM{tc.rule}}, tc.agents, nil)

			got := r.Resolve(context.Background(), tc.asking)

			if tc.want == "" {
				if got.AllowsAny() {
					t.Fatalf("the rule granted something to an agent it does not cover: %+v", got)
				}
				return
			}
			if !got.Allows(tc.want) {
				t.Errorf("the rule did not grant %q: %+v", tc.want, got)
			}
		})
	}
}

// TestResolve_TheTwoSpacesDoNotSeeEachOther pins the property the split exists
// for, at the level the resolver works at.
//
// A rule written against a label cannot be satisfied by the record, and one
// written against a field cannot be satisfied by a tag — which is why no name
// has to be reserved. An agent tagged "workspace=bke" is not in the bke
// workspace, and no selector can make it look otherwise.
func TestResolve_TheTwoSpacesDoNotSeeEachOther(t *testing.T) {
	agents := &fakeAgents{agents: []AgentInfo{
		{Name: "bke-agent", Namespace: "helper-public", Labels: map[string]string{"workspace": "bke"}},
	}}
	ctx := context.Background()

	// A label selector naming "workspace" reads the tag.
	byLabel := newResolver(&fakeRules{rules: []*model.AgentCommandM{
		rule("r", "workspace=bke", "", groupSelector(command.GroupCore), ""),
	}}, agents, nil)
	if got := byLabel.Resolve(ctx, []string{"bke-agent"}); !got.Allows("doctor") {
		t.Errorf("the label selector did not match the tag: %+v", got)
	}

	// The same word in a field selector reads the record instead.
	byField := newResolver(&fakeRules{rules: []*model.AgentCommandM{
		withFields("r", "metadata.workspace=bke", groupSelector(command.GroupCore)),
	}}, agents, nil)
	if got := byField.Resolve(ctx, []string{"bke-agent"}); got.AllowsAny() {
		t.Errorf("a field selector matched the tag: %+v", got)
	}

	// ...and the record's real workspace is what it matches.
	byRealField := newResolver(&fakeRules{rules: []*model.AgentCommandM{
		withFields("r", "metadata.workspace=helper-public", groupSelector(command.GroupCore)),
	}}, agents, nil)
	if got := byRealField.Resolve(ctx, []string{"bke-agent"}); !got.Allows("doctor") {
		t.Errorf("the field selector did not match the record: %+v", got)
	}
}

// TestCoverageOf is the admin report: per agent, which rules cover it and what
// they add up to.
//
// It matters more than it reads. Commands are deny-by-default, so "no rule
// covers this agent" and "a rule covers it and grants nothing" are both an
// empty menu, and the two call for different fixes. A report that showed only
// the command set would make them the same row.
func TestCoverageOf(t *testing.T) {
	agents := &fakeAgents{agents: []AgentInfo{
		{Name: "bke-agent", Labels: map[string]string{"sre": ""}},
		{Name: "plain-agent"},
		{Name: "other-agent"},
	}}
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("core-for-sre", "sre", "", groupSelector(command.GroupCore), ""),
		rule("pod-for-bke", "", `["bke-agent"]`, "", `["pod"]`),
		// Covers other-agent and grants nothing: the misconfiguration the report
		// exists to make visible.
		rule("typo", "", `["other-agent"]`, command.LabelGroup+"=no-such-group", ""),
	}}

	got, err := newResolver(rules, agents, nil).CoverageOf(context.Background())
	if err != nil {
		t.Fatalf("CoverageOf = %v", err)
	}
	if len(got.Agents) != 3 {
		t.Fatalf("Agents = %+v, want every listed agent", got.Agents)
	}
	if got.WithNothing != 2 {
		// It counts both ways an agent ends up with nothing: plain-agent is
		// covered by no rule, and other-agent is covered by a rule that grants
		// nothing. The headline is "runs nothing", not "has no rule" — from a
		// conversation, the two look the same.
		t.Errorf("WithNothing = %d, want 2", got.WithNothing)
	}

	byName := map[string]AgentCoverage{}
	for _, c := range got.Agents {
		byName[c.Agent] = c
	}

	covered := byName["bke-agent"]
	if strings.Join(covered.Rules, ",") != "core-for-sre,pod-for-bke" {
		t.Errorf("bke-agent rules = %v, want both covering rules in read order", covered.Rules)
	}
	// The union is what it is granted, not the intersection: one rule grants the
	// core group, the other grants pod.
	if strings.Join(covered.Commands, ",") != "bind-onex,doctor,pod" {
		t.Errorf("bke-agent commands = %v, want the union of both rules", covered.Commands)
	}

	if empty := byName["other-agent"]; len(empty.Rules) != 1 || len(empty.Commands) != 0 {
		t.Errorf("other-agent = %+v, want one covering rule and no commands", empty)
	}
	if dark := byName["plain-agent"]; len(dark.Rules) != 0 {
		t.Errorf("plain-agent = %+v, want no covering rule", dark)
	}
}

// TestCoverageOf_ReportsSkills pins that the report is the whole grant, not the
// catalog half of it: a rule that reaches skills grants commands the catalog
// does not list, and a report that omitted them would call a working agent dark.
func TestCoverageOf_ReportsSkills(t *testing.T) {
	agents := &fakeAgents{agents: []AgentInfo{{Name: "bke-agent"}}}
	skills := &fakeSkills{skills: map[string][]runtime.SkillCommand{
		"bke-agent": {
			{Name: "weekly-report", Provider: "lark_doc"},
			{Name: "k8s-troubleshoot", Provider: "github"},
		},
	}}
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("skills", "", `["bke-agent"]`, groupSelector(command.GroupSkill), ""),
	}}

	got, err := newResolver(rules, agents, skills).CoverageOf(context.Background())
	if err != nil {
		t.Fatalf("CoverageOf = %v", err)
	}
	if len(got.Agents) != 1 {
		t.Fatalf("Agents = %+v, want one", got.Agents)
	}
	if names := strings.Join(got.Agents[0].Skills, ","); names != "k8s-troubleshoot,weekly-report" {
		t.Errorf("Skills = %v, want both, sorted", got.Agents[0].Skills)
	}
	if got.WithNothing != 0 {
		t.Errorf("WithNothing = %d; an agent granted only skills is not dark", got.WithNothing)
	}
}

// TestCoverageOf_AnUnreadableAgentListIsAnError pins the one answer the report
// must not give: a deployment whose agents could not be read has an unknown
// fleet, and an empty list would say it has none.
func TestCoverageOf_AnUnreadableAgentListIsAnError(t *testing.T) {
	agents := &fakeAgents{err: errors.New("the agent service is down")}
	got, err := newResolver(&fakeRules{}, agents, nil).CoverageOf(context.Background())

	if err == nil {
		t.Fatalf("CoverageOf = %+v, want the read failure", got)
	}
	if got != nil {
		t.Errorf("CoverageOf returned %+v alongside an error, want nil", got)
	}
}

// TestCoverageOf_ReadsTheAgentsFresh pins the same property Preview has: the
// report is wanted in the minute after somebody changed an agent's tags, so it
// reads rather than serving the snapshot a message path has already filled.
func TestCoverageOf_ReadsTheAgentsFresh(t *testing.T) {
	rules := &fakeRules{rules: []*model.AgentCommandM{
		rule("r", "", `["bke-agent"]`, groupSelector(command.GroupCore), ""),
	}}
	agents := &fakeAgents{agents: []AgentInfo{{Name: "bke-agent"}}}
	r := newResolver(rules, agents, nil)

	// Warm the message path's snapshot.
	if got := r.Resolve(context.Background(), []string{"bke-agent"}); !got.Allows("doctor") {
		t.Fatalf("the warm-up resolve did not see the rule: %+v", got)
	}
	before := agents.reads()

	if _, err := r.CoverageOf(context.Background()); err != nil {
		t.Fatalf("CoverageOf = %v", err)
	}
	if agents.reads() != before+1 {
		t.Errorf("the agent service was read %d times, want %d: the report served the snapshot",
			agents.reads(), before+1)
	}
}

// captureLogs redirects the default logger into a buffer for one test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// TestCompileRules_ReportsNamesTheCatalogDoesNotHave pins the one place a rename
// is caught.
//
// The rule is kept — the rest of it still works, and dropping it over one stale
// name would take a whole grant away — so the only thing that can tell anybody
// is this line. Without it the shape is: a command is renamed, every stored rule
// that named the old name quietly grants one command less, and the first symptom
// is a user who cannot run it. That is not hypothetical; it is what two rules in
// the live table did when /bind was renamed to /bind-onex.
//
// The warning has to name the rule and the string: the rule is what to edit, and
// the string is what is wrong with it.
func TestCompileRules_ReportsNamesTheCatalogDoesNotHave(t *testing.T) {
	logs := captureLogs(t)

	rules := compileRules([]*model.AgentCommandM{
		rule("onex", "", `["bke-agent"]`, "", `["bind"]`),
		rule("clean", "", `["bke-agent"]`, "", `["pod"]`),
	})

	if len(rules) != 2 {
		t.Fatalf("compileRules kept %d rules, want both: a stale name is not a broken rule", len(rules))
	}

	got := logs.String()
	if !strings.Contains(got, "onex") || !strings.Contains(got, "bind") {
		t.Errorf("log = %q, want the rule and the name it asked for", got)
	}
	if !strings.Contains(got, "WARN") {
		t.Errorf("log = %q, want a warning: nothing else reports this", got)
	}
	// The rule that names only commands the catalog has is not mentioned, so
	// the warning is about the orphan and not about compiling at all.
	if strings.Contains(got, "clean") {
		t.Errorf("log = %q, want the rule with no orphan left out of it", got)
	}
}
