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

// Package roleauthz answers the two role questions chat asks — "is this
// identity an SRE?", "is this identity an administrator?" — from the shared
// authorization rule table, so role membership is expressed in the same rules
// as every other permission instead of a second list that has to be kept in
// sync by hand.
//
// # The identity is a set, not a string
//
// A rule can name a person or a group. The groups a person belongs to still come
// from chat.setting (it is the roster an operator maintains); this package turns
// that into authz.Subject values and asks the policy:
//
//	read role:sre    with subjects [user:go.kong, group:sre]
//
// so either a per-person rule or a per-group rule grants the role, and a deny
// aimed at the group wins over a grant aimed at the person (see
// Coordinator.AuthorizeUse). chat.setting is therefore demoted to "who is in
// which group", while the rule table decides what a group may do.
//
// # Failure is a refusal
//
// The two methods have no error return because their callers ask a yes/no
// question on the message path. An evaluation error is logged and answered
// false: an unreachable policy engine is not evidence that someone is an
// administrator.
package roleauthz

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// groupCodesTTL bounds how long a membership answer is reused. Membership
// changes are authorization changes, so the value is short; the HTTP call is
// the expensive half and the role question is asked on the message path.
const groupCodesTTL = 30 * time.Second

// groupCodesTimeout bounds one membership lookup. A slow agent service must not
// hold up a message; the answer without groups is the safe one (a group grant
// is withheld, never invented).
const groupCodesTimeout = 2 * time.Second

// Role names the rules use. They are the values a rule's resource_name carries
// (resource_kind "role"), so they are part of the contract with whoever writes
// the rules, not an internal label.
const (
	RoleSRE   = "sre"
	RoleAdmin = "admin"
)

// Resolver is the two-question surface every role consumer in chat already
// speaks (see biz/v1/auth.RoleResolver, which is an alias of this type).
type Resolver interface {
	// IsSREUser reports whether identity may act as an SRE.
	IsSREUser(identity string) bool
	// IsAdminUser reports whether identity may act as an administrator.
	IsAdminUser(identity string) bool
}

// Roster is who is in which group. The live implementation is *roleset.Cache;
// it is an interface here so this package does not depend on the cache's
// concrete surface.
type Roster interface {
	IsSREUser(identity string) bool
	// IsAdminUser is the union of the admin list and the SRE list, which is why
	// an SRE is also offered the admin group below.
	IsAdminUser(identity string) bool
}

// GroupSource resolves an identity to the console-managed user groups it
// belongs to. The live implementation is the agent client; membership lives in
// the agent schema, and this is the signed read that exposes it.
type GroupSource interface {
	UserGroupCodes(ctx context.Context, identity string) ([]string, error)
}

type cachedCodes struct {
	codes   []string
	expires time.Time
}

// Authz answers from the policy. A nil coordinator never reaches these methods:
// New returns the roster unchanged in that case.
type Authz struct {
	roster      Roster
	coordinator *authz.Coordinator
	groups      GroupSource

	mu    sync.RWMutex
	cache map[string]cachedCodes
}

// New returns the resolver a deployment should use: the policy-backed one when
// a coordinator was built, otherwise roster unchanged.
//
// A nil roster is valid (no group source configured): the policy is then asked
// about the user alone, which is how a rule naming a person works with no roster
// at all.
func New(roster Roster, coordinator *authz.Coordinator) Resolver {
	return NewWithGroups(roster, coordinator, nil)
}

// NewWithGroups is New plus the console-managed user groups. A nil groups
// source is the deployment that only uses the deployment roster.
func NewWithGroups(roster Roster, coordinator *authz.Coordinator, groups GroupSource) Resolver {
	if coordinator == nil || !coordinator.Enabled() {
		if roster == nil {
			return denyAll{}
		}
		return roster
	}
	return &Authz{
		roster:      roster,
		coordinator: coordinator,
		groups:      groups,
		cache:       make(map[string]cachedCodes),
	}
}

// denyAll is what a deployment with neither a roster nor a policy gets: no
// roles, which is the same direction as an empty list.
type denyAll struct{}

func (denyAll) IsSREUser(string) bool   { return false }
func (denyAll) IsAdminUser(string) bool { return false }

// IsSREUser implements Resolver.
func (a *Authz) IsSREUser(identity string) bool { return a.allows(identity, RoleSRE) }

// IsAdminUser implements Resolver.
func (a *Authz) IsAdminUser(identity string) bool { return a.allows(identity, RoleAdmin) }

// allows asks the policy about one role, with the caller's groups as extra
// subjects. An empty identity is refused before the policy is consulted: it is a
// caller that could not name itself, and a rule for "" would be a rule for
// nobody.
func (a *Authz) allows(identity, role string) bool {
	if identity == "" {
		return false
	}

	subjects := []authz.Subject{authz.SubjectFromUser(identity, "")}
	if a.roster != nil {
		if a.roster.IsSREUser(identity) {
			subjects = append(subjects, authz.Subject{Kind: authz.SubjectKindGroup, GroupCode: RoleSRE})
		}
		if a.roster.IsAdminUser(identity) {
			subjects = append(subjects, authz.Subject{Kind: authz.SubjectKindGroup, GroupCode: RoleAdmin})
		}
	}
	for _, code := range a.groupCodes(identity) {
		subjects = append(subjects, authz.Subject{Kind: authz.SubjectKindGroup, GroupCode: code})
	}
	subjects = dedupeSubjects(subjects)

	decision, reason, err := a.coordinator.AuthorizeUse(context.Background(), subjects,
		authz.VerbRead, authz.ResourceKindRole, role)
	if err != nil {
		slog.Error("roleauthz: role evaluation failed, refusing the role",
			"identity", identity, "role", role, "error", err)
		return false
	}
	if decision != authz.DecisionAllow {
		slog.Debug("roleauthz: role refused", "identity", identity, "role", role, "reason", reason)
		return false
	}
	return true
}

// groupCodes returns the console-managed groups identity belongs to, cached for
// a short interval. A lookup failure yields no groups and is logged: it can only
// withhold a group grant, never add one.
func (a *Authz) groupCodes(identity string) []string {
	if a.groups == nil || identity == "" {
		return nil
	}
	now := time.Now()
	a.mu.RLock()
	entry, ok := a.cache[identity]
	a.mu.RUnlock()
	if ok && now.Before(entry.expires) {
		return entry.codes
	}

	ctx, cancel := context.WithTimeout(context.Background(), groupCodesTimeout)
	defer cancel()
	codes, err := a.groups.UserGroupCodes(ctx, identity)
	if err != nil {
		slog.Warn("roleauthz: user group lookup failed, continuing without group subjects",
			"identity", identity, "error", err)
		return nil
	}

	a.mu.Lock()
	a.cache[identity] = cachedCodes{codes: append([]string(nil), codes...), expires: now.Add(groupCodesTTL)}
	a.mu.Unlock()
	return codes
}

// dedupeSubjects keeps the first occurrence of each (kind, id) pair. The roster
// may name "admin"/"sre" and a console group may too; the policy only needs the
// subject once.
func dedupeSubjects(in []authz.Subject) []authz.Subject {
	seen := make(map[string]struct{}, len(in))
	out := make([]authz.Subject, 0, len(in))
	for _, subject := range in {
		key := string(subject.Kind) + "/" + subject.PrimaryID()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, subject)
	}
	return out
}
