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

package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/agentref"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// scheduler returns a bot with nothing wired, which is enough: the deciders and
// the allocators are package-level and every input they consult is on the
// Decision. A test that needs a runtime here would be a test proving a decider
// depends on one, which is the arrangement this refactor removed.
func scheduler() *botBiz { return &botBiz{} }

// TestDefaultRefsDependsOnlyOnSessionType pins the shape of each template.
//
// It is asserted per session type and with no audience anywhere in sight,
// because that separation is the point: the template says whether `@default`
// appears at all, and the audience decides what it turns into. A test that only
// checked the resolved result would stay green if the audience rule were folded
// back into the template, which is the arrangement this split replaced.
func TestDefaultRefsDependsOnlyOnSessionType(t *testing.T) {
	tests := []struct {
		sessionType string
		want        []string
	}{
		{model.SessionTypeTicketGroup, []string{"@oncall", "@peer", "@default"}},
		{model.SessionTypePrivateChat, []string{"@peer", "@default"}},
		{model.SessionTypeNormalGroup, []string{"@peer", "@default"}},
		{"", []string{"@default"}},
	}
	for _, tt := range tests {
		got := defaultRefs(tt.sessionType)
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("defaultRefs(%q) = %v, want %v", tt.sessionType, got, tt.want)
		}
	}
}

// TestPrivateTemplateKeepsTheFallback guards the regression the template's
// second entry exists for: an internal user has no binding, so a private chat
// that only asked for `@peer` would resolve to nothing at all — where the
// hardcoded "default" this replaced always answered something.
func TestPrivateTemplateKeepsTheFallback(t *testing.T) {
	if got := defaultRefs(model.SessionTypePrivateChat); len(got) < 2 || got[1] != "@default" {
		t.Fatalf("private-chat template = %v, want a @default fallback after @peer", got)
	}
}

// TestGroupTemplateKeepsTheFallback guards the same regression for a group: a
// group now resolves per speaker, and most speakers in one have no binding —
// were the template `["@peer"]` alone, every unbound member would resolve to
// nothing instead of the platform default.
func TestGroupTemplateKeepsTheFallback(t *testing.T) {
	if got := defaultRefs(model.SessionTypeNormalGroup); len(got) < 2 || got[0] != "@peer" || got[1] != "@default" {
		t.Fatalf("group template = %v, want @peer with a @default fallback", got)
	}
}

// TestOpenAPITemplateAsksForNoPeer pins that the change made for groups did not
// leak into the catch-all: an OpenAPI conversation has no peer identity to look
// up, so asking for @peer there would be asking a question nothing answers.
func TestOpenAPITemplateAsksForNoPeer(t *testing.T) {
	got := defaultRefs(model.SessionTypeOpenAPI)
	if len(got) != 1 || got[0] != "@default" {
		t.Fatalf("openapi template = %v, want the platform default alone", got)
	}
}

// TestConversationTypeOf pins which type a decision uses when the stored row is
// missing or states nothing.
//
// A direct receiver is a fact about the platform, and it is what keeps a private
// chat's `@peer` reachable when the P2P-entered event never created the row.
// A group is deliberately not inferred: its row is created by the bot-added
// event, and a missing one is the state the conservative catch-all exists for.
func TestConversationTypeOf(t *testing.T) {
	tests := []struct {
		name    string
		session *model.SessionM
		isP2P   bool
		want    string
	}{
		{"the stored type wins", &model.SessionM{SessionType: model.SessionTypeNormalGroup}, false, model.SessionTypeNormalGroup},
		{"a stored private type wins even for a group receiver", &model.SessionM{SessionType: model.SessionTypePrivateChat}, false, model.SessionTypePrivateChat},
		{"a missing row on a P2P is a private chat", nil, true, model.SessionTypePrivateChat},
		{"an empty stored type on a P2P is a private chat", &model.SessionM{}, true, model.SessionTypePrivateChat},
		{"a missing row on a group stays unknown", nil, false, ""},
		{"an empty stored type on a group stays unknown", &model.SessionM{}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conversationTypeOf(tt.session, tt.isP2P); got != tt.want {
				t.Errorf("conversationTypeOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestEffectiveRefsPrecedence pins the type-scoped priority rule.
//
// A private chat's usable binding replaces the conversation's declaration: the
// room and the speaker are one person there, and the binding is the statement
// the console shows. A group keeps its declaration first, because there it is a
// room-level decision. An empty binding is not usable — it falls back rather
// than overriding with nothing.
func TestEffectiveRefsPrecedence(t *testing.T) {
	tests := []struct {
		name             string
		sessionType      string
		declared         []string
		sp               PrincipalRef
		want             []string
		wantBindingFirst bool
	}{
		{
			name:             "private chat: a usable binding wins",
			sessionType:      model.SessionTypePrivateChat,
			declared:         []string{"fz-public"},
			sp:               PrincipalRef{PeerBound: true, PeerAgents: []string{"fz-admin"}},
			want:             []string{"@peer"},
			wantBindingFirst: true,
		},
		{
			name:        "private chat: bound but naming nothing falls back to the declaration",
			sessionType: model.SessionTypePrivateChat,
			declared:    []string{"fz-public"},
			sp:          PrincipalRef{PeerBound: true, PeerAgents: []string{}},
			want:        []string{"fz-public"},
		},
		{
			name:        "private chat: a malformed empty name is not usable",
			sessionType: model.SessionTypePrivateChat,
			declared:    []string{"fz-public"},
			sp:          PrincipalRef{PeerBound: true, PeerAgents: []string{""}},
			want:        []string{"fz-public"},
		},
		{
			name:        "private chat: nothing declared and unbound uses the template",
			sessionType: model.SessionTypePrivateChat,
			sp:          PrincipalRef{},
			want:        []string{"@peer", "@default"},
		},
		{
			name:        "normal group: the room's declaration wins over a binding",
			sessionType: model.SessionTypeNormalGroup,
			declared:    []string{"ops-agent"},
			sp:          PrincipalRef{PeerBound: true, PeerAgents: []string{"fz-admin"}},
			want:        []string{"ops-agent"},
		},
		{
			name:        "normal group: no declaration uses the template",
			sessionType: model.SessionTypeNormalGroup,
			sp:          PrincipalRef{PeerBound: true, PeerAgents: []string{"fz-admin"}},
			want:        []string{"@peer", "@default"},
		},
		{
			name:        "ticket group: the declaration still wins over a binding",
			sessionType: model.SessionTypeTicketGroup,
			declared:    []string{"ops-agent"},
			sp:          PrincipalRef{PeerBound: true, PeerAgents: []string{"fz-admin"}},
			want:        []string{"ops-agent"},
		},
		{
			name:        "ticket group: the template puts oncall before the speaker's binding",
			sessionType: model.SessionTypeTicketGroup,
			sp:          PrincipalRef{PeerBound: true, PeerAgents: []string{"fz-admin"}},
			want:        []string{"@oncall", "@peer", "@default"},
		},
		{
			name:        "openapi is unaffected",
			sessionType: model.SessionTypeOpenAPI,
			sp:          PrincipalRef{PeerBound: true, PeerAgents: []string{"fz-admin"}},
			want:        []string{"@default"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, bindingFirst := effectiveRefs(tt.sessionType, tt.declared, tt.sp)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("effectiveRefs() = %v, want %v", got, tt.want)
			}
			if bindingFirst != tt.wantBindingFirst {
				t.Errorf("effectiveRefs() bindingPreferred = %v, want %v", bindingFirst, tt.wantBindingFirst)
			}
		})
	}
}

// TestAudienceOfIsAboutTheSpeaker pins that the audience comes from the
// speaker's tenant identity and nothing else — not the conversation, not the
// session type.
func TestAudienceOfIsAboutTheSpeaker(t *testing.T) {
	tests := []struct {
		name         string
		tenantUserID string
		convType     string
		want         string
	}{
		{"a tenant speaker is internal", "go.kong", model.SessionTypeNormalGroup, AudienceInternal},
		{"a tenant speaker is internal even in a group", "go.kong", model.SessionTypeNormalGroup, AudienceInternal},
		{"a speaker with no tenant identity is mixed", "", model.SessionTypePrivateChat, AudienceMixed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decision{
				Conversation: ConversationRef{Type: tt.convType},
				Speaker:      PrincipalRef{TenantUserID: tt.tenantUserID},
			}
			if got := AudienceOf(d); got != tt.want {
				t.Errorf("AudienceOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestExpand covers the resolution loop: what a static entry does, what an empty
// reference does, and — the reason the loop exists — a reference whose answer is
// another reference.
func TestExpand(t *testing.T) {
	helperInternal := AudienceDefaults{Internal: "helper"}
	helperBoth := AudienceDefaults{Internal: "helper", External: "public-agent"}
	selfRef := AudienceDefaults{Internal: "@default", External: "@default"}

	tests := []struct {
		name     string
		refs     []string
		defaults AudienceDefaults
		sp       PrincipalRef
		want     []string
		wants    string // the error message must contain this, when non-empty
	}{
		{
			name:     "a literal passes through",
			defaults: helperInternal,
			refs:     []string{"ops-agent"},
			want:     []string{"ops-agent"},
		},
		{
			name: "a literal is not checked for existence here",
			// Existence is the /agent command's business. The scheduler's job is
			// to resolve, and an agent that vanished between the two must surface
			// as the agent service's refusal rather than as silence here.
			defaults: helperInternal,
			refs:     []string{"gone-agent"},
			want:     []string{"gone-agent"},
		},
		{
			name:     "peer with no binding resolves to nothing",
			defaults: helperInternal,
			refs:     []string{"@peer"},
			sp:       PrincipalRef{PeerBound: false},
			want:     nil,
		},
		{
			name:     "peer with a binding resolves to its agents",
			defaults: helperInternal,
			refs:     []string{"@peer"},
			sp:       PrincipalRef{PeerBound: true, PeerAgents: []string{"onex-u-user-a1b2c3"}},
			want:     []string{"onex-u-user-a1b2c3"},
		},
		{
			name:     "the first reference that resolves wins the ordering",
			defaults: helperInternal,
			refs:     []string{"@peer", "@default"},
			sp:       PrincipalRef{TenantUserID: "go.kong", PeerBound: true, PeerAgents: []string{"onex-u-user-a1b2c3"}},
			want:     []string{"onex-u-user-a1b2c3", "helper"},
		},
		{
			name:     "internal audience gets the internal default",
			defaults: helperBoth,
			refs:     []string{"@default"},
			sp:       PrincipalRef{TenantUserID: "go.kong"},
			want:     []string{"helper"},
		},
		{
			name:     "mixed audience gets the external default",
			defaults: helperBoth,
			refs:     []string{"@default"},
			sp:       PrincipalRef{TenantUserID: ""},
			want:     []string{"public-agent"},
		},
		{
			name:     "an unconfigured external default resolves to nothing",
			defaults: helperInternal,
			refs:     []string{"@default"},
			sp:       PrincipalRef{TenantUserID: ""},
			want:     nil,
		},
		{
			name: "default configured as a reference is expanded in the next pass",
			// The chain the loop exists for: pass one turns @default into @peer,
			// pass two turns @peer into names. One pass would hand the agent
			// service a literal "@peer", which is not an agent.
			defaults: AudienceDefaults{Internal: "@peer"},
			refs:     []string{"@default"},
			sp:       PrincipalRef{TenantUserID: "go.kong", PeerBound: true, PeerAgents: []string{"onex-u-user-a1b2c3"}},
			want:     []string{"onex-u-user-a1b2c3"},
		},
		{
			name:     "oncall resolves from the conversation",
			defaults: helperInternal,
			refs:     []string{"@oncall", "@default"},
			sp:       PrincipalRef{TenantUserID: "go.kong"},
			want:     []string{"ops-agent", "helper"},
		},
		{
			name:     "repeats collapse to one position",
			defaults: helperInternal,
			refs:     []string{"a", "b", "a"},
			want:     []string{"a", "b"},
		},
		{
			name:     "an unknown kind is refused",
			defaults: helperInternal,
			refs:     []string{"@nope"},
			wants:    "no decider is registered",
		},
		{
			name:     "a decider that takes no argument refuses one",
			defaults: helperInternal,
			refs:     []string{"@peer:whatever"},
			wants:    "takes no argument",
		},
		{
			name:     "a self-referential default is caught by the depth bound",
			defaults: selfRef,
			refs:     []string{"@default"},
			wants:    "exceeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decision{
				Conversation: ConversationRef{OncallAgents: []string{"ops-agent"}},
				Speaker:      tt.sp,
				Defaults:     tt.defaults,
				Refs:         tt.refs,
			}
			got, err := scheduler().expand(context.Background(), d)
			if tt.wants != "" {
				if err == nil {
					t.Fatalf("expand(%v) = %v, want an error containing %q", tt.refs, got, tt.wants)
				}
				if !strings.Contains(err.Error(), tt.wants) {
					t.Fatalf("expand(%v) error = %q, want it to contain %q", tt.refs, err, tt.wants)
				}
				return
			}
			if err != nil {
				t.Fatalf("expand(%v) returned error: %v", tt.refs, err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("expand(%v) = %v, want %v", tt.refs, got, tt.want)
			}
		})
	}
}

// TestAllocators pins the two strategies, and in particular that their empty
// cases differ in the way the interface split exists to express.
func TestAllocators(t *testing.T) {
	reg := allocators
	cands := []string{"a", "b"}

	first := reg[chat.AllocatorFirst]
	if got, _ := first.Allocate(context.Background(), Decision{}, cands); len(got) != 1 || got[0] != "a" {
		t.Errorf("first allocator = %v, want just the leading candidate", got)
	}
	all := reg[chat.AllocatorAll]
	if got, _ := all.Allocate(context.Background(), Decision{}, cands); len(got) != 2 {
		t.Errorf("all allocator = %v, want both candidates", got)
	}
	// Empty in, empty out, from both: neither invents an answer, and the caller
	// decides what an empty result means.
	for _, a := range reg {
		if got, _ := a.Allocate(context.Background(), Decision{}, nil); got != nil {
			t.Errorf("%s allocator invented %v from an empty candidate set", a.Name(), got)
		}
	}
}

// TestHintFor pins which sentence an empty result earns.
func TestHintFor(t *testing.T) {
	tests := []struct {
		name  string
		refs  []string
		sp    PrincipalRef
		final []string
		want  Hint
	}{
		{"a result means no hint", []string{"@peer"}, PrincipalRef{}, []string{"a"}, HintNone},
		{"peer asked for and unbound", []string{"@peer", "@default"}, PrincipalRef{PeerBound: false}, nil, HintUnbound},
		{"peer bound but nothing usable", []string{"@peer"}, PrincipalRef{PeerBound: true}, nil, HintNoAgent},
		{"no peer asked for", []string{"@default"}, PrincipalRef{}, nil, HintUnresolved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decision{Speaker: tt.sp, Refs: tt.refs}
			if got := hintFor(d, tt.final); got != tt.want {
				t.Errorf("hintFor(%v) = %q, want %q", tt.refs, got, tt.want)
			}
		})
	}
}

// TestRegistriesCoverTheVocabulary pins that agentref.Kinds and the decider
// registry agree, from the test side too: the registry panics on a mismatch, so
// reaching the assertions below at all is most of the proof.
func TestRegistriesCoverTheVocabulary(t *testing.T) {
	reg := deciders
	for _, k := range agentref.Kinds() {
		if _, ok := reg[k]; !ok {
			t.Errorf("agentref declares %q but no decider implements it", k)
		}
	}
	if len(reg) != len(agentref.Kinds()) {
		t.Errorf("registry has %d deciders, vocabulary has %d kinds", len(reg), len(agentref.Kinds()))
	}
	if _, ok := allocators[agentref.KindPeer]; ok {
		t.Error("a reference kind leaked into the allocator registry")
	}
}

// TestUnknownAllocatorIsAnError pins that a bad configuration name fails the
// turn rather than silently falling back: falling back would look like a working
// deployment that answers with a strategy nobody chose.
func TestUnknownAllocatorIsAnError(t *testing.T) {
	b := &botBiz{sr: &runtime.StaticRuntime{AgentAllocator: "weighted"}}
	if _, err := b.allocate(context.Background(), Decision{}, []string{"a"}); err == nil {
		t.Fatal("allocate with an unknown strategy returned no error")
	} else if !strings.Contains(err.Error(), "weighted") {
		t.Errorf("error %q does not name the offending strategy", err)
	}
}

// TestExpandReportsParseFailures pins that a malformed entry is refused rather
// than dropped. A dropped entry is an agent set nobody asked for, and it would
// be indistinguishable from one that resolved to nothing.
func TestExpandReportsParseFailures(t *testing.T) {
	_, err := scheduler().expand(context.Background(), Decision{Refs: []string{"@"}})
	if !errors.Is(err, agentref.ErrMalformedRef) {
		t.Fatalf("expand(@) error = %v, want it to wrap ErrMalformedRef", err)
	}
}
