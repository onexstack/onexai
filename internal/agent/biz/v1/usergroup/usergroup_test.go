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

package usergroup

import "testing"

func TestMembersOfAllowsOnePersonWithTheSameValueInTwoAliasFields(t *testing.T) {
	// A local account's username often equals its ID. That is one person, not
	// two members claiming the same identifier.
	members, err := membersOf([]Member{
		{UserID: "dev-user", Username: "dev-user", LarkUserID: "go.kong"},
	})
	if err != nil {
		t.Fatalf("membersOf: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("got %d members, want 1", len(members))
	}
}

func TestMembersOfRejectsTwoMembersSharingAnAlias(t *testing.T) {
	_, err := membersOf([]Member{
		{UserID: "uuid-a", Username: "alice"},
		{UserID: "uuid-b", LarkUserID: "alice"},
	})
	if err == nil {
		t.Fatal("want an error: two members claim the same identifier")
	}
}

func TestMembersOfRejectsAMemberWithNoIdentity(t *testing.T) {
	if _, err := membersOf([]Member{{DisplayName: "who?"}}); err == nil {
		t.Fatal("want an error: a member with no identifier can never match")
	}
}
