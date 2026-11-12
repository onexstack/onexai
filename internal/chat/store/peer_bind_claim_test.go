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

package store

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/model"
)

// The binding handshake's code is typed into a chat, and a chat has an
// audience. Everything below is about the two properties that make that safe:
// whoever sends the code first is who gets asked to confirm — and the binding
// itself still needs the digest that first sender was handed, so reading a code
// over somebody's shoulder buys nothing.

// newPeerBindClaimStoreFor opens a peerBindClaimStore over a fresh database.
func newPeerBindClaimStoreFor(t *testing.T) (*peerBindClaimStore, *gorm.DB) {
	t.Helper()
	db := newStoreTestDB(t, &model.PeerBindClaimM{})
	return newPeerBindClaimStore(&store{db: db}), db
}

// TestPeerBindClaimFirstWriterWins is the whole defence, asserted from both
// sides: the second claim reports that it lost, and — the part that matters —
// the stored identity is still the first one's.
//
// The attack it prevents is concrete. The code is shown on the console and
// typed into a chat, so anyone in that chat can send it first; if the second
// write won, a bystander's chat identity would be bound to the account that
// minted the code.
func TestPeerBindClaimFirstWriterWins(t *testing.T) {
	ctx := context.Background()
	s, db := newPeerBindClaimStoreFor(t)
	const code = "BIND-8f3a21"

	won, _, err := s.Claim(ctx, code, "lark", "cli_app", "ou_victim", 10*time.Minute)
	if err != nil {
		t.Fatalf("first Claim() = %v", err)
	}
	if !won {
		t.Fatal("the first claim of a code reported that it lost")
	}

	won, _, err = s.Claim(ctx, code, "lark", "cli_app", "ou_bystander", 10*time.Minute)
	if err != nil {
		t.Fatalf("second Claim() = %v", err)
	}
	if won {
		t.Error("the second claim of the same code reported that it won")
	}

	got, err := s.GetLive(ctx, code)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if got == nil {
		t.Fatal("Get() found no claim for a code that was just claimed")
	}
	if got.PeerID != "ou_victim" {
		t.Errorf("PeerID = %q, want the first claimant: a later claim replaced the identity", got.PeerID)
	}

	var count int64
	if err := db.Model(&model.PeerBindClaimM{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("%d rows for one code, want 1", count)
	}
}

// TestPeerBindClaimRecordsTheTriple pins what a claim carries: enough to name
// the peer, and nothing about the business account — the chat side never learns
// that, which is what lets the same table serve any business system.
func TestPeerBindClaimRecordsTheTriple(t *testing.T) {
	ctx := context.Background()
	s, _ := newPeerBindClaimStoreFor(t)

	before := time.Now()
	if _, _, err := s.Claim(ctx, "BIND-1", "lark", "cli_app", "ou_alice", 10*time.Minute); err != nil {
		t.Fatalf("Claim() = %v", err)
	}

	got, err := s.GetLive(ctx, "BIND-1")
	if err != nil || got == nil {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	if got.Backend != "lark" || got.Scope != "cli_app" || got.PeerID != "ou_alice" {
		t.Errorf("claim = %+v, want the triple the peer was looked up by", got)
	}
	// The deadline is the caller's, measured from the moment the chat side heard
	// the code — which is the only moment it can measure.
	if d := got.ExpiresAt.Sub(got.ClaimedAt); d != 10*time.Minute {
		t.Errorf("ExpiresAt - ClaimedAt = %v, want the requested 10m", d)
	}
	if got.ClaimedAt.Before(before.Add(-time.Second)) {
		t.Errorf("ClaimedAt = %v, want it stamped at the claim (%v)", got.ClaimedAt, before)
	}
}

// TestPeerBindClaimGetHidesExpiredRows pins that expiry is applied by the store
// rather than left to the caller. A caller that forgot to compare ExpiresAt
// would bind a peer to a code that stopped meaning anything, and the code is
// the only thing tying the two halves of the handshake together.
func TestPeerBindClaimGetHidesExpiredRows(t *testing.T) {
	ctx := context.Background()
	s, _ := newPeerBindClaimStoreFor(t)

	// A lapsed deadline, produced through the same call the bot makes: the store
	// takes the TTL from its caller and does not second-guess whether the code
	// should already have expired.
	if _, _, err := s.Claim(ctx, "BIND-STALE", "lark", "cli_app", "ou_alice", -time.Second); err != nil {
		t.Fatalf("Claim() = %v", err)
	}

	got, err := s.GetLive(ctx, "BIND-STALE")
	if err != nil {
		t.Fatalf("Get() on an expired claim = %v, want no error", err)
	}
	if got != nil {
		t.Errorf("Get() on an expired claim = %+v, want nil: an expired code must read as 'nothing to find'", got)
	}

	// And a code nobody ever spoke is the same answer.
	if got, err := s.GetLive(ctx, "BIND-NEVER"); err != nil || got != nil {
		t.Errorf("Get() on an unknown code = %+v, %v, want a miss", got, err)
	}
}

// TestPeerBindClaimCleanupExpiredSweepsOnlyLapsedRows pins the sweep. Nothing
// consumes a row on read, so a failed binding attempt leaves one behind every
// time — this is what keeps the table from growing without bound, and it must
// not take a live claim with it.
func TestPeerBindClaimCleanupExpiredSweepsOnlyLapsedRows(t *testing.T) {
	ctx := context.Background()
	s, db := newPeerBindClaimStoreFor(t)

	for _, code := range []string{"BIND-STALE-1", "BIND-STALE-2"} {
		if _, _, err := s.Claim(ctx, code, "lark", "cli_app", "ou_"+code, -time.Minute); err != nil {
			t.Fatalf("Claim(%s) = %v", code, err)
		}
	}
	if _, _, err := s.Claim(ctx, "BIND-LIVE", "lark", "cli_app", "ou_alice", 10*time.Minute); err != nil {
		t.Fatalf("Claim(live) = %v", err)
	}

	removed, err := s.CleanupExpired(ctx)
	if err != nil {
		t.Fatalf("CleanupExpired() = %v", err)
	}
	if removed != 2 {
		t.Errorf("CleanupExpired() removed %d rows, want the 2 lapsed ones", removed)
	}

	var count int64
	if err := db.Model(&model.PeerBindClaimM{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("%d rows left, want only the live claim", count)
	}
	if got, err := s.GetLive(ctx, "BIND-LIVE"); err != nil || got == nil {
		t.Errorf("the live claim did not survive the sweep: %+v, %v", got, err)
	}

	// A second sweep with nothing to do is not an error, and reports nothing.
	removed, err = s.CleanupExpired(ctx)
	if err != nil {
		t.Fatalf("second CleanupExpired() = %v", err)
	}
	if removed != 0 {
		t.Errorf("second CleanupExpired() removed %d rows, want 0", removed)
	}
}

// TestPeerBindClaimWithoutAKeyIsANoop pins the guards for the same reason as
// the binding store's: a row keyed by an empty code is one the business side
// can never come back and read, so writing it would be silent garbage.
func TestPeerBindClaimWithoutAKeyIsANoop(t *testing.T) {
	ctx := context.Background()
	s, db := newPeerBindClaimStoreFor(t)

	for _, tc := range []struct{ code, peerID string }{
		{"", "ou_alice"},
		{"BIND-1", ""},
	} {
		won, _, err := s.Claim(ctx, tc.code, "lark", "cli_app", tc.peerID, time.Minute)
		if err != nil {
			t.Errorf("Claim(%q, %q) = %v, want a silent no-op", tc.code, tc.peerID, err)
		}
		if won {
			t.Errorf("Claim(%q, %q) claimed to have won", tc.code, tc.peerID)
		}
	}

	var count int64
	if err := db.Model(&model.PeerBindClaimM{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("%d rows written for claims that cannot be read back", count)
	}

	if got, err := s.GetLive(ctx, ""); err != nil || got != nil {
		t.Errorf("Get(\"\") = %+v, %v, want a miss", got, err)
	}
}

// digitsOnly reports whether s is a non-empty run of decimal digits.
func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// TestPeerBindClaimStoresADigestNotTheCode pins both ends of the confirmation
// code: what the winner is handed, and what the table keeps.
//
// The plaintext exists only in the message the bot spoke. Storing it would put
// the one half a leaked binding code is missing into the same database the claim
// itself lives in — a reader of this table could then complete a binding that
// the code leak alone could not.
func TestPeerBindClaimStoresADigestNotTheCode(t *testing.T) {
	ctx := context.Background()
	s, _ := newPeerBindClaimStoreFor(t)

	won, confirm, err := s.Claim(ctx, "BIND-1", "lark", "cli_app", "ou_alice", 10*time.Minute)
	if err != nil {
		t.Fatalf("Claim() = %v", err)
	}
	if !won {
		t.Fatal("Claim() reported that it lost")
	}
	// A person reads this off one screen and types it into another; anything
	// that is not digits is something they can get wrong.
	if !digitsOnly(confirm) || len(confirm) != confirmCodeDigits {
		t.Errorf("confirmation code = %q, want %d decimal digits", confirm, confirmCodeDigits)
	}

	got, err := s.GetLive(ctx, "BIND-1")
	if err != nil || got == nil {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	if got.ConfirmHash == "" {
		t.Fatal("the claim carries no confirmation digest, so nothing can ever be confirmed")
	}
	if got.ConfirmHash == confirm {
		t.Error("the claim stores the confirmation code verbatim")
	}
	if want := hashConfirm(confirm); got.ConfirmHash != want {
		t.Errorf("ConfirmHash = %q, want the digest of the code (%q)", got.ConfirmHash, want)
	}
	// And a second claim draws a different one — a digest that were constant
	// across claims would be a constant, not a confirmation.
	won, other, err := s.Claim(ctx, "BIND-2", "lark", "cli_app", "ou_alice", 10*time.Minute)
	if err != nil || !won {
		t.Fatalf("second Claim() = %v, %v", won, err)
	}
	if other == confirm {
		t.Errorf("two claims drew the same confirmation code (%q)", other)
	}
}

// TestPeerBindClaimLoserGetsNoConfirmationCode is the property the whole second
// half rests on: a bystander who read the code first may hold a claim, but it
// buys them a confirmation for nobody but themselves.
func TestPeerBindClaimLoserGetsNoConfirmationCode(t *testing.T) {
	ctx := context.Background()
	s, _ := newPeerBindClaimStoreFor(t)

	if _, _, err := s.Claim(ctx, "BIND-1", "lark", "cli_app", "ou_alice", 10*time.Minute); err != nil {
		t.Fatalf("first Claim() = %v", err)
	}
	won, confirm, err := s.Claim(ctx, "BIND-1", "lark", "cli_app", "ou_bystander", 10*time.Minute)
	if err != nil {
		t.Fatalf("second Claim() = %v", err)
	}
	if won {
		t.Fatal("the second claim of one code reported that it won")
	}
	if confirm != "" {
		t.Errorf("the losing claim was handed a confirmation code (%q); confirming a claim must require having made it", confirm)
	}
}

// TestPeerBindClaimReissueRotatesOnlyForTheNamedPeer pins the repeat path.
//
// Only the digest is stored, so the words of the first code cannot be said
// again — a repeat has to mint a new one. That makes the predicate load-bearing:
// rotating a claim a stranger names would be a way to invalidate the
// confirmation code of the person the claim actually belongs to.
func TestPeerBindClaimReissueRotatesOnlyForTheNamedPeer(t *testing.T) {
	ctx := context.Background()
	s, _ := newPeerBindClaimStoreFor(t)

	_, first, err := s.Claim(ctx, "BIND-1", "lark", "cli_app", "ou_alice", 10*time.Minute)
	if err != nil {
		t.Fatalf("Claim() = %v", err)
	}
	before, err := s.GetLive(ctx, "BIND-1")
	if err != nil || before == nil {
		t.Fatalf("Get() = %+v, %v", before, err)
	}

	confirm, ok, err := s.Reissue(ctx, "BIND-1", "ou_bystander")
	if err != nil {
		t.Fatalf("Reissue() for a stranger = %v", err)
	}
	if ok {
		t.Error("Reissue rotated a claim for a peer it does not name")
	}
	if confirm != "" {
		t.Errorf("Reissue handed a stranger a confirmation code (%q)", confirm)
	}
	if after, _ := s.GetLive(ctx, "BIND-1"); after.ConfirmHash != before.ConfirmHash {
		t.Error("a stranger's repeat changed the digest")
	}

	second, ok, err := s.Reissue(ctx, "BIND-1", "ou_alice")
	if err != nil {
		t.Fatalf("Reissue() = %v", err)
	}
	if !ok {
		t.Fatal("Reissue refused the peer the claim names")
	}
	if second == first {
		t.Error("Reissue handed back the same code, so a lost message could never be replaced")
	}
	after, err := s.GetLive(ctx, "BIND-1")
	if err != nil || after == nil {
		t.Fatalf("Get() = %+v, %v", after, err)
	}
	if want := hashConfirm(second); after.ConfirmHash != want {
		t.Errorf("ConfirmHash = %q, want the digest of the reissued code (%q)", after.ConfirmHash, want)
	}
	if after.ConfirmHash == before.ConfirmHash {
		t.Error("the row still carries the superseded digest, so the old code would still pass")
	}
}

// TestPeerBindClaimReissueFillsARowThatHasNoDigestYet is the migration story for
// a binding already in flight when this column appears: its row carries no
// digest and can never be confirmed, but one repeat from the peer it names is
// enough to give it one. Nothing has to be backfilled.
func TestPeerBindClaimReissueFillsARowThatHasNoDigestYet(t *testing.T) {
	ctx := context.Background()
	s, db := newPeerBindClaimStoreFor(t)

	if err := db.Create(&model.PeerBindClaimM{
		Code: "BIND-LEGACY", Backend: "lark", Scope: "cli_app", PeerID: "ou_alice",
		ClaimedAt: time.Now(), ExpiresAt: time.Now().Add(10 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed a row written before the column existed: %v", err)
	}

	confirm, ok, err := s.Reissue(ctx, "BIND-LEGACY", "ou_alice")
	if err != nil {
		t.Fatalf("Reissue() = %v", err)
	}
	if !ok || confirm == "" {
		t.Fatalf("Reissue() = %q, %v, want a code for a live row the peer names", confirm, ok)
	}
	got, err := s.GetLive(ctx, "BIND-LEGACY")
	if err != nil || got == nil {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	if got.ConfirmHash != hashConfirm(confirm) {
		t.Errorf("ConfirmHash = %q, want the digest of the issued code", got.ConfirmHash)
	}
}

// TestPeerBindClaimReissueWithoutALiveRowIsANoop pins that nothing is written
// when there is nothing to rotate — including for a lapsed row the sweeper has
// not collected yet, which Get already reports as absent.
func TestPeerBindClaimReissueWithoutALiveRowIsANoop(t *testing.T) {
	ctx := context.Background()
	s, db := newPeerBindClaimStoreFor(t)

	if err := db.Create(&model.PeerBindClaimM{
		Code: "BIND-STALE", Backend: "lark", Scope: "cli_app", PeerID: "ou_alice",
		ClaimedAt: time.Now().Add(-2 * time.Minute), ExpiresAt: time.Now().Add(-time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed a lapsed row: %v", err)
	}

	for _, tc := range []struct{ name, code, peerID string }{
		{"lapsed row", "BIND-STALE", "ou_alice"},
		{"unknown code", "BIND-NEVER", "ou_alice"},
		{"no code", "", "ou_alice"},
		{"no peer", "BIND-STALE", ""},
	} {
		confirm, ok, err := s.Reissue(ctx, tc.code, tc.peerID)
		if err != nil {
			t.Errorf("%s: Reissue() = %v, want a silent no-op", tc.name, err)
		}
		if ok || confirm != "" {
			t.Errorf("%s: Reissue() = %q, %v, want nothing issued", tc.name, confirm, ok)
		}
	}

	// And the lapsed row was not brought back to life by the attempt.
	var count int64
	if err := db.Model(&model.PeerBindClaimM{}).Where("confirm_hash <> ''").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("%d rows gained a digest from an attempt that should have written nothing", count)
	}
}
