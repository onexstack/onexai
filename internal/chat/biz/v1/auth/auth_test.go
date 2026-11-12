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

package auth

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/password"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/oidc"
)

const testJWTSecret = "test-secret-value"

// noRoles is the deployment that configured no role lists. It is the default a
// test should start from, and the safe direction: nobody holds a role.
var noRoles = RoleSets{}

// fakeUsers is the slice of the account store the login rules use.
type fakeUsers struct {
	byName   map[string]*model.UserM
	byID     map[string]*model.UserM
	bindings map[string]*model.UserLarkBindingM // keyed by open_id
}

func (f *fakeUsers) Create(_ context.Context, obj *model.UserM) error {
	if _, ok := f.byName[obj.Username]; ok {
		return errDuplicate{}
	}
	if f.byName == nil {
		f.byName = map[string]*model.UserM{}
		f.byID = map[string]*model.UserM{}
	}
	f.byName[obj.Username] = obj
	if f.byID == nil {
		f.byID = map[string]*model.UserM{}
	}
	f.byID[obj.ID] = obj
	return nil
}

func (f *fakeUsers) Update(_ context.Context, obj *model.UserM) error {
	if f.byName == nil {
		f.byName = map[string]*model.UserM{}
		f.byID = map[string]*model.UserM{}
	}
	f.byName[obj.Username] = obj
	f.byID[obj.ID] = obj
	return nil
}

func (f *fakeUsers) GetByUsername(_ context.Context, username string) (*model.UserM, error) {
	if u, ok := f.byName[username]; ok {
		return u, nil
	}
	return nil, store.ErrRecordNotFound
}

func (f *fakeUsers) GetByID(_ context.Context, id string) (*model.UserM, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, store.ErrRecordNotFound
}

func (f *fakeUsers) CountAll(context.Context) (int64, error) {
	return int64(len(f.byID)), nil
}

func (f *fakeUsers) LarkBindingByOpenID(_ context.Context, openID string) (*model.UserLarkBindingM, error) {
	if b, ok := f.bindings[openID]; ok {
		return b, nil
	}
	return nil, nil
}

func (f *fakeUsers) PutLarkBinding(_ context.Context, m *model.UserLarkBindingM) error {
	if f.bindings == nil {
		f.bindings = map[string]*model.UserLarkBindingM{}
	}
	f.bindings[m.OpenID] = m
	return nil
}

func (f *fakeUsers) LarkBinding(_ context.Context, userID string) (*model.UserLarkBindingM, error) {
	for _, b := range f.bindings {
		if b.UserID == userID {
			return b, nil
		}
	}
	return nil, nil
}

type errDuplicate struct{}

func (errDuplicate) Error() string { return "duplicate key value violates unique constraint" }

// testDirectory returns a directory with the three accounts the local-login
// rules are exercised with. The password hash is computed once so the test's
// bcrypt cost is not multiplied by the number of cases.
func testDirectory(t *testing.T) *fakeUsers {
	t.Helper()
	hash, err := password.Hash("anything")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	users := &fakeUsers{byName: map[string]*model.UserM{}, byID: map[string]*model.UserM{}}
	for _, name := range []string{"admin", "admin-user", "sre-user"} {
		user := &model.UserM{
			ID:           "id-" + name,
			Username:     name,
			PasswordHash: hash,
			DisplayName:  name,
			Status:       model.UserStatusActive,
			Provider:     "local",
		}
		users.byName[name] = user
		users.byID[user.ID] = user
	}
	return users
}

func login(t *testing.T, providers ProviderOptions, roles RoleSets, req *LoginRequest) (*LoginResponse, error) {
	t.Helper()
	return New(testJWTSecret, providers, roles, testDirectory(t), nil, nil).Login(context.Background(), req)
}

// TestDisabledProvidersMintNoIdentity pins the default posture: with nothing
// enabled, every provider is refused before any credential is examined, so an
// arbitrary non-empty string cannot be traded for a signed identity token. This
// is the authentication-bypass regression test.
func TestDisabledProvidersMintNoIdentity(t *testing.T) {
	requests := map[string]*LoginRequest{
		"lark":   {Provider: "lark", LarkToken: "anything"},
		"local":  {Provider: "local", Username: "admin", Password: "anything"},
		"apikey": {Provider: "apikey", APIKey: "anything"},
	}

	for name, req := range requests {
		t.Run(name, func(t *testing.T) {
			resp, err := login(t, ProviderOptions{}, noRoles, req)
			if err == nil {
				t.Fatalf("provider %q issued a token while disabled: %+v", name, resp)
			}
			if !strings.Contains(err.Error(), "disabled") {
				t.Errorf("error %q does not say the provider is disabled", err)
			}
		})
	}
}

// TestOffListLocalUserGrantsNoRoles is the successor of the guarantee that a
// local login never carried is_sre.
//
// The rule changed deliberately: a role is now granted when the deployment's
// list names the identity, rather than never. What must still hold — and what
// this pins — is the other half: an identity that no list names gets neither
// role, so enabling the local provider does not by itself make anyone an
// administrator.
func TestOffListLocalUserGrantsNoRoles(t *testing.T) {
	roles := RoleSets{
		SRE:   map[string]struct{}{"someone-else": {}},
		Admin: map[string]struct{}{"someone-else": {}},
	}
	resp, err := login(t, ProviderOptions{AllowLocal: true}, roles,
		&LoginRequest{Provider: "local", Username: "admin", Password: "anything"})
	if err != nil {
		t.Fatalf("enabled local provider refused a login: %v", err)
	}

	claims := decode(t, resp.Token)
	if claims.IsSRE || claims.IsAdmin {
		t.Errorf("an unlisted identity was granted a role: sre=%v admin=%v", claims.IsSRE, claims.IsAdmin)
	}
	if claims.Provider != "local" {
		t.Errorf("provider claim = %q, want local", claims.Provider)
	}
	if claims.Name != "admin" {
		t.Errorf("name claim = %q, want the provider's name", claims.Name)
	}
}

// TestListedIdentityCarriesBothRoles pins the rule in the direction that makes
// the management surface usable: the deployment names an identity, and the token
// it gets says so.
func TestListedIdentityCarriesBothRoles(t *testing.T) {
	roles := RoleSets{
		SRE:   map[string]struct{}{"sre-user": {}},
		Admin: map[string]struct{}{"sre-user": {}, "admin-user": {}},
	}

	t.Run("admin only", func(t *testing.T) {
		resp, err := login(t, ProviderOptions{AllowLocal: true}, roles,
			&LoginRequest{Provider: "local", Username: "admin-user", Password: "anything"})
		if err != nil {
			t.Fatalf("login refused: %v", err)
		}
		claims := decode(t, resp.Token)
		if !claims.IsAdmin {
			t.Error("an identity on the admin list did not get is_admin")
		}
		if claims.IsSRE {
			t.Error("an identity not on the SRE list got is_sre; the two lists grant different things")
		}
	})

	t.Run("sre gets both", func(t *testing.T) {
		resp, err := login(t, ProviderOptions{AllowLocal: true}, roles,
			&LoginRequest{Provider: "local", Username: "sre-user", Password: "anything"})
		if err != nil {
			t.Fatalf("login refused: %v", err)
		}
		claims := decode(t, resp.Token)
		if !claims.IsSRE {
			t.Error("an identity on the SRE list did not get is_sre")
		}
		if !claims.IsAdmin {
			t.Error("an identity on the SRE list did not get is_admin; the admin set is the union built in providers.roleSets")
		}
	})
}

// TestLarkIdentityIsMatchedAgainstTheRoleLists documents that the lark provider's
// opaque string is what the lists are keyed on — which is what makes the role
// lists usable from the browser at all, and is also the reason this provider
// must stay off except where the deployment means it.
func TestLarkIdentityIsMatchedAgainstTheRoleLists(t *testing.T) {
	roles := RoleSets{Admin: map[string]struct{}{"ou_listed": {}}}

	resp, err := login(t, ProviderOptions{AllowLark: true}, roles,
		&LoginRequest{Provider: "lark", LarkToken: "ou_listed"})
	if err != nil {
		t.Fatalf("login refused: %v", err)
	}
	claims := decode(t, resp.Token)
	if claims.OpenID != "ou_listed" {
		t.Errorf("open_id claim = %q, want the supplied token", claims.OpenID)
	}
	if !claims.IsAdmin {
		t.Error("an identity on the admin list did not get is_admin")
	}
}

// TestAPIKeyProviderGrantsNoRoles pins that a key is not a person: the provider
// names nobody, so it can match no list, however the deployment is configured.
func TestAPIKeyProviderGrantsNoRoles(t *testing.T) {
	roles := RoleSets{
		SRE:   map[string]struct{}{"": {}},
		Admin: map[string]struct{}{"": {}},
	}
	resp, err := login(t, ProviderOptions{AllowAPIKey: true}, roles,
		&LoginRequest{Provider: "apikey", APIKey: "anything"})
	if err != nil {
		t.Fatalf("login refused: %v", err)
	}
	claims := decode(t, resp.Token)
	if claims.IsSRE || claims.IsAdmin {
		t.Errorf("the api-key provider granted a role: sre=%v admin=%v", claims.IsSRE, claims.IsAdmin)
	}
}

// TestUnknownProviderIsRejected pins that the switch still refuses a provider it
// does not know, enabled or not.
func TestUnknownProviderIsRejected(t *testing.T) {
	if _, err := login(t, ProviderOptions{AllowLocal: true, AllowLark: true, AllowAPIKey: true}, noRoles,
		&LoginRequest{Provider: "saml"}); err == nil {
		t.Fatal("an unsupported provider was accepted")
	}
}

// TestIssueAddsAVerifiableOIDCToken pins the additive contract: the login
// response keeps the HS256 browser token and, when the deployment enabled OIDC,
// also carries an RS256 token a Kubernetes authenticator can verify.
func TestIssueAddsAVerifiableOIDCToken(t *testing.T) {
	signer, err := oidc.New(oidc.Config{IssuerURL: "https://auth.test", Audiences: []string{"onexai"}})
	if err != nil {
		t.Fatalf("build signer: %v", err)
	}
	biz := New(testJWTSecret, ProviderOptions{TokenTTL: time.Hour}, noRoles, testDirectory(t), nil, signer)

	resp, err := biz.issue(context.Background(), issueInput{
		userID: "go.kong", name: "Kong", provider: "local", role: model.UserRoleAdmin,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("the browser token is empty")
	}
	if resp.OIDCToken == "" {
		t.Fatal("the OIDC token is empty on an OIDC-enabled deployment")
	}

	verified, err := signer.Verify(resp.OIDCToken)
	if err != nil {
		t.Fatalf("verify OIDC token: %v", err)
	}
	if verified.Subject != "go.kong" {
		t.Fatalf("got subject %q, want go.kong", verified.Subject)
	}
	if !slices.Contains(verified.Groups, "system:masters") {
		t.Fatalf("got groups %v, want system:masters for an administrator", verified.Groups)
	}
}

func decode(t *testing.T, token string) *bearer.Claims {
	t.Helper()
	claims := &bearer.Claims{}
	if _, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) {
		return []byte(testJWTSecret), nil
	}); err != nil {
		t.Fatalf("issued token did not verify: %v", err)
	}
	return claims
}
