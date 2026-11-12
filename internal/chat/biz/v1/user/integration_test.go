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

package user_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	chatapi "github.com/onexstack/onexai/internal/chat/api"
	authn "github.com/onexstack/onexai/internal/chat/biz/v1/auth"
	"github.com/onexstack/onexai/internal/chat/biz/v1/user"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/pkg/dbschema"
)

const (
	testAppID  = "cli_test_app"
	testOpenID = "ou_alice"
	testSecret = "integration-test-secret"
	testPasswd = "password-123"
)

// testStore builds one SQLite-backed store for the whole test. The store
// package keeps a process-wide singleton, so this is also why the test has to
// be the only one that calls NewStore in this binary.
func testStore(t *testing.T) store.IStore {
	t.Helper()
	dbschema.Configure(dbschema.MySQL)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		if strings.Contains(err.Error(), "requires cgo") || strings.Contains(err.Error(), "stub") {
			t.Skipf("sqlite requires cgo: %v", err)
		}
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.AutoMigrate(
		&model.UserM{},
		&model.UserLarkBindingM{},
		&model.UserBindCodeM{},
		&model.PeerAgentM{},
		&model.PeerBindClaimM{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return store.NewStore(db)
}

// TestRegisterLoginAndFeishuBinding is the flow the console drives: open an
// account, sign in with it, bind Feishu through the bot handshake, then unbind.
func TestRegisterLoginAndFeishuBinding(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	auth := authn.New(testSecret, authn.ProviderOptions{
		AllowLocal:        true,
		AllowRegistration: true,
		BootstrapAdmin:    true,
		TokenTTL:          time.Hour,
	}, authn.RoleSets{}, st.Users(), nil, nil)

	registered, err := auth.Register(ctx, &authn.RegisterRequest{
		Username: "alice",
		Password: testPasswd,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if registered.Token == "" || registered.UserID == "" {
		t.Fatalf("Register returned an empty token/user: %+v", registered)
	}

	// The first account is the bootstrap administrator; otherwise the
	// user-management page it unlocks would be unreachable.
	userRow, err := st.Users().GetByID(ctx, registered.UserID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if userRow.Role != model.UserRoleAdmin {
		t.Errorf("first account role = %q, want %q", userRow.Role, model.UserRoleAdmin)
	}

	if _, err := auth.Login(ctx, &authn.LoginRequest{
		Provider: "local", Username: "alice", Password: "wrong-password",
	}); err == nil {
		t.Fatal("login with a wrong password succeeded")
	}
	if _, err := auth.Login(ctx, &authn.LoginRequest{
		Provider: "local", Username: "alice", Password: testPasswd,
	}); err != nil {
		t.Fatalf("Login: %v", err)
	}

	before, err := auth.Me(ctx, &authn.MeRequest{UserID: registered.UserID})
	if err != nil || before.User == nil {
		t.Fatalf("Me before bind: %v, %+v", err, before)
	}
	if before.Feishu != nil {
		t.Fatalf("Me before bind reports a Feishu identity: %+v", before.Feishu)
	}

	ub := user.New(st, testAppID, 10*time.Minute)

	start, err := ub.FeishuBindStart(ctx, &user.BindStartRequest{UserID: registered.UserID})
	if err != nil {
		t.Fatalf("FeishuBindStart: %v", err)
	}
	if !strings.HasPrefix(start.Command, "/bind-onex ") {
		t.Fatalf("bind command = %q", start.Command)
	}

	// The bot hears the command in a private chat and parks its half.
	_, confirm, err := st.PeerBindClaims().Claim(
		ctx, start.Code, "lark", testAppID, testOpenID, 15*time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if confirm == "" {
		t.Fatal("the bot did not issue a confirmation code")
	}

	status, err := ub.FeishuBindStatus(ctx, &user.BindStatusRequest{
		UserID: registered.UserID, Code: start.Code,
	})
	if err != nil {
		t.Fatalf("FeishuBindStatus: %v", err)
	}
	if !status.AwaitingConfirm || status.Bound || status.Expired {
		t.Fatalf("status = %+v, want awaiting_confirm", status)
	}

	if _, err := ub.FeishuBindConfirm(ctx, &user.BindConfirmRequest{
		UserID: registered.UserID, Code: start.Code, Confirm: "00000000",
	}); err == nil {
		t.Fatal("a wrong confirmation code completed the binding")
	}

	if _, err := ub.FeishuBindConfirm(ctx, &user.BindConfirmRequest{
		UserID: registered.UserID, Code: start.Code, Confirm: confirm,
	}); err != nil {
		t.Fatalf("FeishuBindConfirm: %v", err)
	}

	binding, err := st.Users().LarkBinding(ctx, registered.UserID)
	if err != nil || binding == nil {
		t.Fatalf("binding after confirm: %v, %+v", err, binding)
	}
	if binding.OpenID != testOpenID {
		t.Errorf("bound open_id = %q, want %q", binding.OpenID, testOpenID)
	}

	peer, err := st.PeerAgents().GetByPeer(ctx, "lark", testAppID, testOpenID)
	if err != nil || peer == nil {
		t.Fatalf("peer binding after confirm: %v, %+v", err, peer)
	}
	if peer.Canonical != registered.UserID || peer.Source != "console" {
		t.Errorf("peer binding = %+v, want canonical=%s source=console", peer, registered.UserID)
	}

	after, err := auth.Me(ctx, &authn.MeRequest{UserID: registered.UserID})
	if err != nil || after.Feishu == nil || after.Feishu.OpenID != testOpenID {
		t.Fatalf("Me after bind = %+v, %v; want open_id %s", after, err, testOpenID)
	}

	// The console's detail page reads one account by ID. It must carry the
	// same Feishu binding as the list row and fail closed for unknown IDs.
	detail, err := ub.Get(ctx, &user.GetRequest{UserID: registered.UserID})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.ID != registered.UserID || detail.Username != "alice" {
		t.Errorf("Get returned %+v, want alice/%s", detail, registered.UserID)
	}
	if detail.Feishu == nil || detail.Feishu.OpenID != testOpenID {
		t.Errorf("Get Feishu = %+v, want open_id %s", detail.Feishu, testOpenID)
	}
	if _, err := ub.Get(ctx, &user.GetRequest{UserID: "  "}); !errors.Is(err, errno.ErrInvalidArgument) {
		t.Errorf("Get with an empty id: %v, want ErrInvalidArgument", err)
	}
	if _, err := ub.Get(ctx, &user.GetRequest{UserID: "ghost"}); !errors.Is(err, errno.ErrNotFound) {
		t.Errorf("Get with an unknown id: %v, want ErrNotFound", err)
	}

	// The code and its claim are single-use.
	if again, err := ub.FeishuBindStatus(ctx, &user.BindStatusRequest{
		UserID: registered.UserID, Code: start.Code,
	}); err != nil || !again.Bound {
		t.Fatalf("status after bind = %+v, %v", again, err)
	}

	if err := ub.FeishuUnbind(ctx, &user.UnbindRequest{UserID: registered.UserID}); err != nil {
		t.Fatalf("FeishuUnbind: %v", err)
	}
	if binding, _ := st.Users().LarkBinding(ctx, registered.UserID); binding != nil {
		t.Fatal("the binding survived an unbind")
	}
	if peer, _ := st.PeerAgents().GetByPeer(ctx, "lark", testAppID, testOpenID); peer != nil {
		t.Fatal("the peer binding survived an unbind")
	}

	// A peer row whose canonical id names no account is what an account delete
	// used to leave behind, and what an external identity system can push. It
	// must not hold the Feishu identity hostage: the account holder just proved
	// they speak as it, so the row is adopted rather than reported as a
	// conflict.
	t.Run("stale peer binding is adopted", func(t *testing.T) {
		const staleOpenID = "ou_stale"
		if err := st.PeerAgents().Put(ctx, &model.PeerAgentM{
			Backend:   "lark",
			Scope:     testAppID,
			PeerID:    staleOpenID,
			Canonical: "user-deleted",
			Agents:    `["onex-x-max-tier"]`,
			Source:    "onex-iam",
			UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("seed stale peer binding: %v", err)
		}

		bob, err := auth.Register(ctx, &authn.RegisterRequest{
			Username: "bob", Password: testPasswd,
		})
		if err != nil {
			t.Fatalf("Register bob: %v", err)
		}

		code, confirm := startBind(t, ub, st, bob.UserID, staleOpenID)
		if _, err := ub.FeishuBindConfirm(ctx, &user.BindConfirmRequest{
			UserID: bob.UserID, Code: code, Confirm: confirm,
		}); err != nil {
			t.Fatalf("FeishuBindConfirm over a stale peer binding: %v", err)
		}

		peer, err := st.PeerAgents().GetByPeer(ctx, "lark", testAppID, staleOpenID)
		if err != nil || peer == nil {
			t.Fatalf("peer binding after adopt: %v, %+v", err, peer)
		}
		if peer.Canonical != bob.UserID {
			t.Errorf("adopted canonical = %q, want %q", peer.Canonical, bob.UserID)
		}
		if peer.Source != "console" {
			t.Errorf("adopted source = %q, want console", peer.Source)
		}
		if peer.Agents != `["onex-x-max-tier"]` {
			t.Errorf("adopted agents = %q, want the identity's existing list", peer.Agents)
		}

		if err := ub.FeishuUnbind(ctx, &user.UnbindRequest{UserID: bob.UserID}); err != nil {
			t.Fatalf("FeishuUnbind after adopt: %v", err)
		}
		if peer, _ := st.PeerAgents().GetByPeer(ctx, "lark", testAppID, staleOpenID); peer != nil {
			t.Fatal("the adopted peer binding survived an unbind")
		}
	})

	// The adoption path must not weaken the guard: a canonical that still names
	// a live account is a real conflict, and the handshake stays retryable.
	t.Run("peer binding to a live account is a conflict", func(t *testing.T) {
		const liveOpenID = "ou_live"
		carol, err := auth.Register(ctx, &authn.RegisterRequest{
			Username: "carol", Password: testPasswd,
		})
		if err != nil {
			t.Fatalf("Register carol: %v", err)
		}
		dave, err := auth.Register(ctx, &authn.RegisterRequest{
			Username: "dave", Password: testPasswd,
		})
		if err != nil {
			t.Fatalf("Register dave: %v", err)
		}
		if err := st.PeerAgents().Put(ctx, &model.PeerAgentM{
			Backend:   "lark",
			Scope:     testAppID,
			PeerID:    liveOpenID,
			Canonical: carol.UserID,
			Agents:    `["onex-x-max-tier"]`,
			Source:    "onex-iam",
			UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("seed live peer binding: %v", err)
		}

		code, confirm := startBind(t, ub, st, dave.UserID, liveOpenID)
		_, err = ub.FeishuBindConfirm(ctx, &user.BindConfirmRequest{
			UserID: dave.UserID, Code: code, Confirm: confirm,
		})
		if !errors.Is(err, errno.ErrAlreadyExists) {
			t.Fatalf("FeishuBindConfirm over a live peer binding = %v, want AlreadyExists", err)
		}

		if binding, _ := st.Users().LarkBinding(ctx, dave.UserID); binding != nil {
			t.Fatalf("a refused binding was still written: %+v", binding)
		}
		peer, err := st.PeerAgents().GetByPeer(ctx, "lark", testAppID, liveOpenID)
		if err != nil || peer == nil {
			t.Fatalf("live peer binding after refusal: %v, %+v", err, peer)
		}
		if peer.Canonical != carol.UserID || peer.Source != "onex-iam" {
			t.Errorf("live peer binding was rewritten: %+v", peer)
		}
		if claim, err := st.PeerBindClaims().GetLive(ctx, code); err != nil || claim == nil {
			t.Errorf("the claim was consumed by a refused confirm: %+v, %v", claim, err)
		}
		if record, err := st.Users().GetLiveBindCode(ctx, code, dave.UserID); err != nil || record == nil {
			t.Errorf("the bind code was consumed by a refused confirm: %+v, %v", record, err)
		}
	})

	// Deleting an account releases the Feishu identity everywhere, including the
	// bot's peer projection, so the same identity can be attached elsewhere.
	t.Run("deleting an account removes its peer binding", func(t *testing.T) {
		const deleteOpenID = "ou_delete"
		erin, err := auth.Register(ctx, &authn.RegisterRequest{
			Username: "erin", Password: testPasswd,
		})
		if err != nil {
			t.Fatalf("Register erin: %v", err)
		}
		code, confirm := startBind(t, ub, st, erin.UserID, deleteOpenID)
		if _, err := ub.FeishuBindConfirm(ctx, &user.BindConfirmRequest{
			UserID: erin.UserID, Code: code, Confirm: confirm,
		}); err != nil {
			t.Fatalf("FeishuBindConfirm for erin: %v", err)
		}

		if err := ub.Delete(ctx, &user.DeleteRequest{
			ActorID: registered.UserID, UserID: erin.UserID,
		}); err != nil {
			t.Fatalf("Delete erin: %v", err)
		}
		if peer, _ := st.PeerAgents().GetByPeer(ctx, "lark", testAppID, deleteOpenID); peer != nil {
			t.Fatalf("the peer binding outlived the account: %+v", peer)
		}
	})

	// The console's peer-binding view: self-service edits its own row, an
	// administrator sees and edits everyone's, and a row written by onex-iam is
	// read-only because rewriting it would create two authorities.
	t.Run("peer binding management", func(t *testing.T) {
		const frankOpenID = "ou_frank"
		frank, err := auth.Register(ctx, &authn.RegisterRequest{
			Username: "frank", Password: testPasswd,
		})
		if err != nil {
			t.Fatalf("Register frank: %v", err)
		}
		code, confirm := startBind(t, ub, st, frank.UserID, frankOpenID)
		if _, err := ub.FeishuBindConfirm(ctx, &user.BindConfirmRequest{
			UserID: frank.UserID, Code: code, Confirm: confirm,
		}); err != nil {
			t.Fatalf("FeishuBindConfirm for frank: %v", err)
		}

		mg := user.NewWithCatalog(st, testAppID, 10*time.Minute, fakeAgentCatalog{
			agents: []chatapi.AgentItem{
				{Name: "helper", Status: "enabled"},
				{Name: "assistant", Status: "enabled"},
				{Name: "retired", Status: "disabled"},
			},
		})

		// Self view: exactly one row, fully editable.
		self, err := mg.ListFeishuBindings(ctx, &user.ListFeishuBindingsRequest{
			ActorID: frank.UserID, Page: 1, PageSize: 20,
		})
		if err != nil {
			t.Fatalf("ListFeishuBindings as frank: %v", err)
		}
		if self.Total != 1 || len(self.Bindings) != 1 {
			t.Fatalf("frank's list = %+v, want one row", self)
		}
		item := self.Bindings[0]
		if item.PeerID != frankOpenID || item.State != user.PeerStateBound {
			t.Errorf("frank's row = %+v, want %s bound", item, frankOpenID)
		}
		if !item.Writable || !item.CanUnbind {
			t.Errorf("frank's row is not editable: writable=%v can_unbind=%v", item.Writable, item.CanUnbind)
		}
		if len(item.Agents) != 0 {
			t.Errorf("agents after binding = %v, want empty", item.Agents)
		}
		if item.Account == nil || item.Account.Username != "frank" {
			t.Errorf("account = %+v, want frank", item.Account)
		}

		// A non-administrator cannot ask for somebody else's rows.
		if _, err := mg.ListFeishuBindings(ctx, &user.ListFeishuBindingsRequest{
			ActorID: frank.UserID, UserID: registered.UserID, Page: 1, PageSize: 20,
		}); !errors.Is(err, errno.ErrPermissionDenied) {
			t.Fatalf("listing another user's bindings = %v, want PermissionDenied", err)
		}

		// The administrator sees the whole deployment.
		all, err := mg.ListFeishuBindings(ctx, &user.ListFeishuBindingsRequest{
			ActorID: registered.UserID, IsAdmin: true, Page: 1, PageSize: 100,
		})
		if err != nil {
			t.Fatalf("ListFeishuBindings as admin: %v", err)
		}
		if !hasPeer(all.Bindings, frankOpenID) {
			t.Errorf("the admin view does not contain %s: %+v", frankOpenID, all.Bindings)
		}

		// A user_id filter narrows both halves of the join: the account binding
		// and any peer-only row. Otherwise an administrator filtering to one
		// account would still see every orphan in the deployment.
		scoped, err := mg.ListFeishuBindings(ctx, &user.ListFeishuBindingsRequest{
			ActorID: registered.UserID, IsAdmin: true, UserID: frank.UserID,
			Page: 1, PageSize: 100,
		})
		if err != nil {
			t.Fatalf("ListFeishuBindings filtered by user: %v", err)
		}
		for _, item := range scoped.Bindings {
			if item.Canonical != frank.UserID {
				t.Errorf("user_id filter returned a row for %q: %+v", item.Canonical, item)
			}
		}
		if !hasPeer(scoped.Bindings, frankOpenID) {
			t.Errorf("user_id filter dropped frank's own row: %+v", scoped.Bindings)
		}

		// Self-service edit, including de-duplication.
		updated, err := mg.UpdateFeishuAgents(ctx, &user.UpdateFeishuAgentsRequest{
			ActorID: frank.UserID, PeerID: frankOpenID,
			Agents: []string{"helper", " helper "},
		})
		if err != nil {
			t.Fatalf("UpdateFeishuAgents as frank: %v", err)
		}
		if len(updated.Agents) != 1 || updated.Agents[0] != "helper" {
			t.Errorf("agents after update = %v, want [helper]", updated.Agents)
		}
		row, err := st.PeerAgents().GetByPeer(ctx, "lark", testAppID, frankOpenID)
		if err != nil || row == nil {
			t.Fatalf("peer row after update: %v, %+v", err, row)
		}
		if row.Agents != `["helper"]` || row.Source != "console" {
			t.Errorf("stored row = %+v, want agents [helper] source console", row)
		}

		// One Feishu identity maps to one agent: a second, distinct name is refused.
		if _, err := mg.UpdateFeishuAgents(ctx, &user.UpdateFeishuAgentsRequest{
			ActorID: frank.UserID, PeerID: frankOpenID,
			Agents: []string{"helper", "assistant"},
		}); !errors.Is(err, errno.ErrInvalidArgument) ||
			!strings.Contains(err.Error(), "只能绑定 1 个智能体") {
			t.Fatalf("two agents = %v, want InvalidArgument about the single-agent limit", err)
		}

		// New names are validated against the same directory the bot uses.
		if _, err := mg.UpdateFeishuAgents(ctx, &user.UpdateFeishuAgentsRequest{
			ActorID: frank.UserID, PeerID: frankOpenID, Agents: []string{"nope"},
		}); !errors.Is(err, errno.ErrInvalidArgument) {
			t.Fatalf("unknown agent = %v, want InvalidArgument", err)
		}
		if _, err := mg.UpdateFeishuAgents(ctx, &user.UpdateFeishuAgentsRequest{
			ActorID: frank.UserID, PeerID: frankOpenID, Agents: []string{"retired"},
		}); !errors.Is(err, errno.ErrInvalidArgument) {
			t.Fatalf("disabled agent = %v, want InvalidArgument", err)
		}

		// An outsider cannot read or write the row.
		if _, err := mg.GetFeishuBinding(ctx, &user.GetFeishuBindingRequest{
			ActorID: frank.UserID, PeerID: "ou_live",
		}); !errors.Is(err, errno.ErrNotFound) {
			t.Fatalf("reading another user's row = %v, want NotFound", err)
		}

		// A row written by onex-iam is read-only in both directions.
		row.Source = "onex-iam"
		if err := st.PeerAgents().Put(ctx, row); err != nil {
			t.Fatalf("mark row onex-iam: %v", err)
		}
		if _, err := mg.UpdateFeishuAgents(ctx, &user.UpdateFeishuAgentsRequest{
			ActorID: frank.UserID, PeerID: frankOpenID, Agents: []string{"helper"},
		}); !errors.Is(err, errno.ErrPermissionDenied) {
			t.Fatalf("editing an onex-iam row = %v, want PermissionDenied", err)
		}
		if err := mg.UnbindFeishuBinding(ctx, &user.UnbindFeishuBindingRequest{
			ActorID: registered.UserID, IsAdmin: true, PeerID: frankOpenID,
		}); !errors.Is(err, errno.ErrPermissionDenied) {
			t.Fatalf("unbinding an onex-iam row = %v, want PermissionDenied", err)
		}
		row.Source = "console"
		if err := st.PeerAgents().Put(ctx, row); err != nil {
			t.Fatalf("restore console source: %v", err)
		}

		// The administrator can edit on the user's behalf.
		if _, err := mg.UpdateFeishuAgents(ctx, &user.UpdateFeishuAgentsRequest{
			ActorID: registered.UserID, IsAdmin: true, PeerID: frankOpenID,
			Agents: []string{},
		}); err != nil {
			t.Fatalf("admin UpdateFeishuAgents: %v", err)
		}
		if row, _ := st.PeerAgents().GetByPeer(ctx, "lark", testAppID, frankOpenID); row.Agents != "" {
			t.Errorf("cleared agents = %q, want empty", row.Agents)
		}

		// An account binding with no peer row is account_only: visible, not
		// editable, still unbindable.
		const graceOpenID = "ou_grace"
		grace, err := auth.Register(ctx, &authn.RegisterRequest{
			Username: "grace", Password: testPasswd,
		})
		if err != nil {
			t.Fatalf("Register grace: %v", err)
		}
		graceCode, graceConfirm := startBind(t, ub, st, grace.UserID, graceOpenID)
		if _, err := ub.FeishuBindConfirm(ctx, &user.BindConfirmRequest{
			UserID: grace.UserID, Code: graceCode, Confirm: graceConfirm,
		}); err != nil {
			t.Fatalf("FeishuBindConfirm for grace: %v", err)
		}
		if err := st.PeerAgents().DeleteByPeer(ctx, "lark", testAppID, graceOpenID); err != nil {
			t.Fatalf("drop grace's peer row: %v", err)
		}
		graceItem, err := mg.GetFeishuBinding(ctx, &user.GetFeishuBindingRequest{
			ActorID: registered.UserID, IsAdmin: true, PeerID: graceOpenID,
		})
		if err != nil {
			t.Fatalf("GetFeishuBinding for grace: %v", err)
		}
		if graceItem.State != user.PeerStateAccountOnly || graceItem.Writable || !graceItem.CanUnbind {
			t.Errorf("grace's row = %+v, want account_only/writable=false/can_unbind=true", graceItem)
		}
		if graceItem.ReadonlyReason != user.PeerReadonlyNoPeer {
			t.Errorf("grace's readonly reason = %q, want %q", graceItem.ReadonlyReason, user.PeerReadonlyNoPeer)
		}

		// A row naming a different account is a mismatch and is not editable.
		row, _ = st.PeerAgents().GetByPeer(ctx, "lark", testAppID, frankOpenID)
		row.Canonical = registered.UserID
		row.Source = "console"
		if err := st.PeerAgents().Put(ctx, row); err != nil {
			t.Fatalf("seed mismatch: %v", err)
		}
		mismatch, err := mg.GetFeishuBinding(ctx, &user.GetFeishuBindingRequest{
			ActorID: registered.UserID, IsAdmin: true, PeerID: frankOpenID,
		})
		if err != nil {
			t.Fatalf("GetFeishuBinding mismatch: %v", err)
		}
		if mismatch.State != user.PeerStateMismatch || mismatch.Writable {
			t.Errorf("mismatch row = %+v, want mismatch/read-only", mismatch)
		}

		// An administrator unbinding a mismatch removes both halves.
		if err := mg.UnbindFeishuBinding(ctx, &user.UnbindFeishuBindingRequest{
			ActorID: registered.UserID, IsAdmin: true, PeerID: frankOpenID,
		}); err != nil {
			t.Fatalf("admin UnbindFeishuBinding: %v", err)
		}
		if binding, _ := st.Users().LarkBinding(ctx, frank.UserID); binding != nil {
			t.Errorf("frank's account binding survived the admin unbind: %+v", binding)
		}
		if row, _ := st.PeerAgents().GetByPeer(ctx, "lark", testAppID, frankOpenID); row != nil {
			t.Errorf("frank's peer row survived the admin unbind: %+v", row)
		}
	})

	// A row whose canonical names no account is a leftover: the account was
	// deleted, or a migration carried an id this deployment never had. No live
	// authorization depends on it, so an administrator must be able to remove it
	// even when another system wrote it — otherwise it can never be cleaned up.
	t.Run("orphan peer binding is removable by an administrator", func(t *testing.T) {
		const orphanOpenID = "ou_orphan"
		orphan := &model.PeerAgentM{
			Backend:   "lark",
			Scope:     testAppID,
			PeerID:    orphanOpenID,
			Canonical: "user-deleted",
			Agents:    `["fz-admin"]`,
			Source:    "onex",
			UpdatedAt: time.Now(),
		}
		if err := st.PeerAgents().Put(ctx, orphan); err != nil {
			t.Fatalf("seed orphan peer binding: %v", err)
		}

		mg := user.New(st, testAppID, 10*time.Minute)
		item, err := mg.GetFeishuBinding(ctx, &user.GetFeishuBindingRequest{
			ActorID: registered.UserID, IsAdmin: true, PeerID: orphanOpenID,
		})
		if err != nil {
			t.Fatalf("GetFeishuBinding orphan: %v", err)
		}
		if item.Account != nil {
			t.Fatalf("orphan row has an account: %+v", item.Account)
		}
		if !item.CanUnbind {
			t.Errorf("orphan row can_unbind = false, want true for an administrator")
		}

		// The same row stays invisible, hence unremovable, for an ordinary user.
		outsider, err := auth.Register(ctx, &authn.RegisterRequest{
			Username: "orphan-outsider", Password: testPasswd,
		})
		if err != nil {
			t.Fatalf("Register outsider: %v", err)
		}
		if err := mg.UnbindFeishuBinding(ctx, &user.UnbindFeishuBindingRequest{
			ActorID: outsider.UserID, PeerID: orphanOpenID,
		}); !errors.Is(err, errno.ErrNotFound) {
			t.Fatalf("non-admin unbinding an orphan = %v, want NotFound", err)
		}
		if row, _ := st.PeerAgents().GetByPeer(ctx, "lark", testAppID, orphanOpenID); row == nil {
			t.Fatal("a non-administrator removed the orphan row")
		}

		if err := mg.UnbindFeishuBinding(ctx, &user.UnbindFeishuBindingRequest{
			ActorID: registered.UserID, IsAdmin: true, PeerID: orphanOpenID,
		}); err != nil {
			t.Fatalf("admin UnbindFeishuBinding orphan: %v", err)
		}
		if row, _ := st.PeerAgents().GetByPeer(ctx, "lark", testAppID, orphanOpenID); row != nil {
			t.Errorf("the orphan row survived an administrator unbind: %+v", row)
		}
	})

	// The contact record is resolved before the binding is written: it carries
	// the tenant user_id the notify path needs and the name the console shows.
	t.Run("binding stores the resolved Feishu profile", func(t *testing.T) {
		const profileOpenID = "ou_grace_profile"
		profileUser, err := auth.Register(ctx, &authn.RegisterRequest{
			Username: "grace-profile", Password: testPasswd,
		})
		if err != nil {
			t.Fatalf("Register grace-profile: %v", err)
		}
		resolver := &fakeProfileResolver{profile: model.LarkProfile{
			LarkUserID: "grace-user-id",
			UnionID:    "on-grace",
			Name:       "Grace Hopper",
			AvatarURL:  "https://x/grace.png",
		}}
		biz := user.NewWithCatalog(st, testAppID, 10*time.Minute, nil).WithProfileResolver(resolver)

		code, confirm := startBind(t, biz, st, profileUser.UserID, profileOpenID)
		if _, err := biz.FeishuBindConfirm(ctx, &user.BindConfirmRequest{
			UserID: profileUser.UserID, Code: code, Confirm: confirm,
		}); err != nil {
			t.Fatalf("FeishuBindConfirm for grace-profile: %v", err)
		}

		binding, err := st.Users().LarkBinding(ctx, profileUser.UserID)
		if err != nil || binding == nil {
			t.Fatalf("binding for grace-profile: %v, %+v", err, binding)
		}
		if binding.LarkUserID != "grace-user-id" || binding.UnionID != "on-grace" ||
			binding.Name != "Grace Hopper" || binding.AvatarURL != "https://x/grace.png" {
			t.Errorf("binding = %+v, want the resolved profile", binding)
		}
	})

	t.Run("binding succeeds when the profile lookup fails", func(t *testing.T) {
		const heidiOpenID = "ou_heidi"
		heidi, err := auth.Register(ctx, &authn.RegisterRequest{
			Username: "heidi-profile", Password: testPasswd,
		})
		if err != nil {
			t.Fatalf("Register heidi-profile: %v", err)
		}
		resolver := &fakeProfileResolver{err: errors.New("contact API unavailable")}
		biz := user.NewWithCatalog(st, testAppID, 10*time.Minute, nil).WithProfileResolver(resolver)

		code, confirm := startBind(t, biz, st, heidi.UserID, heidiOpenID)
		if _, err := biz.FeishuBindConfirm(ctx, &user.BindConfirmRequest{
			UserID: heidi.UserID, Code: code, Confirm: confirm,
		}); err != nil {
			t.Fatalf("FeishuBindConfirm must not fail when the profile lookup does: %v", err)
		}

		binding, err := st.Users().LarkBinding(ctx, heidi.UserID)
		if err != nil || binding == nil {
			t.Fatalf("binding for heidi: %v, %+v", err, binding)
		}
		if binding.OpenID != heidiOpenID {
			t.Errorf("bound open_id = %q, want %q", binding.OpenID, heidiOpenID)
		}
		if binding.LarkUserID != "" {
			t.Errorf("LarkUserID = %q, want empty after a failed lookup", binding.LarkUserID)
		}
	})
}

// fakeAgentCatalog is the directory the console validates names against.
type fakeAgentCatalog struct {
	agents []chatapi.AgentItem
}

func (f fakeAgentCatalog) ListAgent(context.Context) (*chatapi.ListAgentResponse, error) {
	return &chatapi.ListAgentResponse{Agents: f.agents}, nil
}

func hasPeer(items []user.FeishuBindingItem, peerID string) bool {
	for _, item := range items {
		if item.PeerID == peerID {
			return true
		}
	}
	return false
}

// startBind drives the console half of the handshake up to the point where the
// bot has parked its claim and spoken a confirmation code.
func startBind(t *testing.T, ub user.UserBiz, st store.IStore, userID, openID string) (code, confirm string) {
	t.Helper()
	ctx := context.Background()

	start, err := ub.FeishuBindStart(ctx, &user.BindStartRequest{UserID: userID})
	if err != nil {
		t.Fatalf("FeishuBindStart for %s: %v", openID, err)
	}
	_, confirm, err = st.PeerBindClaims().Claim(
		ctx, start.Code, "lark", testAppID, openID, 15*time.Minute)
	if err != nil {
		t.Fatalf("Claim for %s: %v", openID, err)
	}
	if confirm == "" {
		t.Fatalf("the bot did not issue a confirmation code for %s", openID)
	}
	return start.Code, confirm
}
