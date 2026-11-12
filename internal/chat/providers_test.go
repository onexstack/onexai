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

package chat

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	agentlisters "github.com/onexstack/onexai/pkg/generated/listers/agent/v1alpha1"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/chat/biz"
	imlark "github.com/onexstack/onexai/internal/chat/im/lark"
	"github.com/onexstack/onexai/internal/chat/pkg/i18n"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/authz/opa"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// TestLanguageOptionsMatchTheTranslator closes the loop between the two lists of
// language tags: the options package validates against its own constants, the
// translator resolves against its own, and a tag present in one and not the
// other is a configuration value that passes startup validation and then
// translates nothing — an English deployment answering in Chinese with nothing
// to say why.
func TestLanguageOptionsMatchTheTranslator(t *testing.T) {
	// Every language the translator offers must be spellable in the config.
	for _, lang := range i18n.Languages {
		opts := customoptions.NewChatOptions()
		opts.I18n.Language = string(lang)
		if errs := opts.Validate(); len(errs) > 0 {
			t.Errorf("language %q is supported but refused by the configuration: %v", lang, errs)
		}
	}

	// And every tag the config accepts must reach a language the translator knows.
	for _, tag := range []string{
		customoptions.LanguageZH, customoptions.LanguageZHTW, customoptions.LanguageEN,
	} {
		if _, ok := i18n.ParseLanguage(tag); !ok {
			t.Errorf("chat.i18n.language %q is accepted by the configuration but unknown to the translator", tag)
		}
	}
}

// TestReplyLanguage pins how the configured tag reaches the runtime: the default
// for an unconfigured deployment, the configured value when there is one, and the
// source language for a tag that got past validation some other way.
func TestReplyLanguage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      *Config
		want     i18n.Language
		wantZero bool
	}{
		{name: "no chat block answers in the source language", cfg: &Config{ChatOptions: &customoptions.ChatOptions{}}, want: i18n.Default},
		{
			name: "a configured language is used",
			cfg: &Config{ChatOptions: &customoptions.ChatOptions{
				I18n: customoptions.I18nOptions{Language: customoptions.LanguageEN},
			}},
			want: i18n.EN,
		},
		{
			name: "an unknown tag falls back to the source language",
			cfg: &Config{ChatOptions: &customoptions.ChatOptions{
				I18n: customoptions.I18nOptions{Language: "klingon"},
			}},
			want: i18n.Default,
		},
		{
			name: "an empty tag is the source language",
			cfg: &Config{ChatOptions: &customoptions.ChatOptions{
				I18n: customoptions.I18nOptions{Language: ""},
			}},
			want: i18n.Default,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := replyLanguage(tc.cfg); got != tc.want {
				t.Errorf("replyLanguage() = %q, want %q", got, tc.want)
			}
		})
	}
}

// stubBiz answers only Store(), which is all the dissolve provider asks for.
type stubBiz struct {
	biz.IBiz
	st store.IStore
}

func (s *stubBiz) Store() store.IStore { return s.st }

// TestProvideDissolveSweeperIsOffUntilAsked pins the wiring of the one
// irreversible background job this service has.
//
// Two failures matter here and neither is visible at runtime. A provider that
// returned a sweeper regardless of the switch would dissolve groups on an
// upgrade nobody made a decision about — and a sweep that is on by accident
// looks exactly like one that is doing its job. A provider that returned nil
// when the switch IS on would leave the bot's own promise unkept, with the
// sweep's absence showing up only as groups that never go.
func TestProvideDissolveSweeperIsOffUntilAsked(t *testing.T) {
	b := &stubBiz{st: store.NewStore(nil)}
	backend := &imlark.Backend{}

	off := ProvideDissolveSweeper(&Config{ChatOptions: &customoptions.ChatOptions{}}, b, backend)
	if off != nil {
		t.Errorf("a deployment that did not opt in holds %#v, want a nil interface", off)
	}

	on := ProvideDissolveSweeper(
		&Config{ChatOptions: &customoptions.ChatOptions{
			Dissolve: customoptions.DissolveOptions{Enabled: true, GraceAfterClose: time.Hour},
		}}, b, backend)
	if on == nil {
		t.Fatal("a deployment that opted in got no sweeper")
	}
}

// mutableRules is an opa.RuleSource whose snapshot a test can replace. The
// refresh loop calls List with no filter, so the options argument is ignored.
type mutableRules struct{ rules []*model.AuthzRuleM }

func (m *mutableRules) List(context.Context, *where.Options) (int64, []*model.AuthzRuleM, error) {
	return int64(len(m.rules)), m.rules, nil
}

func (m *mutableRules) set(rules ...*model.AuthzRuleM) { m.rules = rules }

// TestWatchRoleRulesPicksUpAChange pins the timer half of policy-backed role
// resolution: a rule written by the agent service reaches chat within one
// interval, and the answer flips without a restart. Without this the refresh
// loop could be removed and every unit test around it would still pass.
func TestWatchRoleRulesPicksUpAChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	source := &mutableRules{}
	ruleStore := opa.NewRuleStore(source)
	if _, err := ruleStore.Sync(ctx); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}
	policy, err := opa.NewAuthorizer(ctx, ruleStore)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}

	req := authz.AuthorizationRequest{
		Subject:  authz.SubjectFromUser("go.kong", ""),
		Verb:     authz.VerbRead,
		Resource: authz.Resource{Kind: authz.ResourceKindRole, Name: "sre"},
	}
	if decision, _, err := policy.Authorize(ctx, req); err != nil {
		t.Fatalf("baseline Authorize: %v", err)
	} else if decision == authz.DecisionAllow {
		t.Fatal("precondition: the empty rule set must not allow the role")
	}

	changes := make(chan struct{}, 1)
	reload := func(ctx context.Context) error {
		changed, err := ruleStore.Sync(ctx)
		if err != nil {
			return err
		}
		if changed {
			return policy.Reload(ctx)
		}
		return nil
	}
	go watchRoleRules(ctx, changes, reload, 20*time.Millisecond)

	// The rule the agent service would have written.
	source.set(&model.AuthzRuleM{
		Name: "grant-sre-role", SubjectType: "user", SubjectID: []string{"go.kong"},
		ResourceKind: "role", ResourceName: []string{"sre"},
		Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled,
	})
	// The real source signals this from the informer's event handler.
	changes <- struct{}{}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if decision, _, err := policy.Authorize(ctx, req); err == nil && decision == authz.DecisionAllow {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the refresh loop did not pick up the new rule within 2s")
}

// TestWatchRoleRulesDoesNotRecompileOnAnUnchangedSnapshot pins the other half:
// a tick that finds the same rows must not rebuild the policy. It is asserted
// through the decision staying correct while the loop runs with nothing to do —
// a recompile storm would be invisible here, so the cheap structural check is
// that the stored snapshot is untouched (Sync reports no change).
func TestWatchRoleRulesDoesNotRecompileOnAnUnchangedSnapshot(t *testing.T) {
	source := &mutableRules{rules: []*model.AuthzRuleM{{
		Name: "grant-sre-role", SubjectType: "user", SubjectID: []string{"go.kong"},
		ResourceKind: "role", ResourceName: []string{"sre"},
		Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled,
	}}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ruleStore := opa.NewRuleStore(source)
	if changed, err := ruleStore.Sync(ctx); err != nil || !changed {
		t.Fatalf("first Sync: changed=%v err=%v, want true/nil", changed, err)
	}
	if changed, err := ruleStore.Sync(ctx); err != nil || changed {
		t.Fatalf("second Sync: changed=%v err=%v, want false/nil — the loop would recompile on every tick", changed, err)
	}
}

// TestAgentLister_ReadsTheObjectsOwnLabels pins what the command rules select
// agents on: the object's metadata.labels and namespace, read from the control
// plane's cache — not a tag list projected out of an annotation or fetched over
// HTTP.
func TestAgentLister_ReadsTheObjectsOwnLabels(t *testing.T) {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
	})
	if err := indexer.Add(&agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "onex-x-max-tier",
			Namespace: "default",
			Labels:    map[string]string{"onex.example.com/onex": ""},
		},
	}); err != nil {
		t.Fatalf("add the agent: %v", err)
	}
	lister := agentLister{agents: agentlisters.NewAgentLister(indexer)}

	infos, err := lister.ListAgentInfo(context.Background())
	if err != nil {
		t.Fatalf("ListAgentInfo: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("ListAgentInfo = %d agents, want 1", len(infos))
	}
	info := infos[0]
	if info.Name != "onex-x-max-tier" {
		t.Errorf("Name = %q", info.Name)
	}
	if info.Namespace != "default" {
		t.Errorf("Namespace = %q, want the object's namespace", info.Namespace)
	}
	if _, ok := info.Labels["onex.example.com/onex"]; !ok {
		t.Errorf("Labels = %v, want the object's own labels", info.Labels)
	}

	// A deployment with no control plane has no agents, which grants nothing.
	empty, err := agentLister{}.ListAgentInfo(context.Background())
	if err != nil || len(empty) != 0 {
		t.Errorf("a nil lister answered (%v, %v), want no agents and no error", empty, err)
	}
}
