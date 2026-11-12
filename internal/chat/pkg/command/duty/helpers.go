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

package duty

import (
	"context"
	"fmt"
	"strings"
	"time"

	dutystore "github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/chatname"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// groupNotFoundMessage is the answer for a module the store does not know at all.
func groupNotFoundMessage(arg string) string {
	return fmt.Sprintf("值班组 [%s] 不存在，请使用 /oncall --list 查看可用的值班组", arg)
}

// groupWithoutRotationMessage is the answer for a module that exists but has no
// on-call watcher right now. Keeping it distinct from groupNotFoundMessage matters:
// "不存在" (does not exist) sends the user looking for a module that is actually registered.
func groupWithoutRotationMessage(arg string) string {
	return fmt.Sprintf("值班组 [%s] 当前没有值班人，请稍后再试或联系值班管理员", arg)
}

// groupServiceUnavailableMessage is the answer when the store holds no group at
// all: the deployment has no group metadata and the rotation source could not
// be reached, so no name can be judged against anything. Kept apart from
// groupNotFoundMessage so a user whose input was fine is not sent looking for a
// typo while the service, not they, is the problem.
func groupServiceUnavailableMessage() string {
	return "值班服务暂时不可用，请稍后重试；若持续出现请联系值班管理员"
}

// rosterUnavailable reports whether the store could not read the on-call
// rosters from its backend on the last refresh.
//
// Such a store still lists the duty groups it holds, but none of them is known
// to be staffed, so an empty rotation or an empty module list says "the service
// cannot tell" rather than "no such module" or "nobody is on call". The two
// need different answers: the first is a service problem the user can retry,
// the second one they cannot fix from the chat.
func rosterUnavailable(sr *runtime.StaticRuntime) bool {
	return sr.Duty.RosterError() != nil
}

// requireActiveGroup resolves a duty group named on the command line for the
// commands that can only act on an active rotation (/oncall, /hand, /invite).
// It is the one place the failure modes are told apart, so the four command
// surfaces cannot drift apart in wording.
func requireActiveGroup(ctx context.Context, sr *runtime.StaticRuntime, arg string) (dutystore.Group, error) {
	group, ok, err := sr.Duty.GetGroupInfo(ctx, strings.ToLower(arg))
	if err != nil {
		return dutystore.Group{}, runtime.NewUserError("%s", groupServiceUnavailableMessage())
	}
	if !ok {
		known, kerr := sr.Duty.HasKnownGroups(ctx)
		if kerr != nil || !known {
			return dutystore.Group{}, runtime.NewUserError("%s", groupServiceUnavailableMessage())
		}
		return dutystore.Group{}, runtime.NewUserError("%s", groupNotFoundMessage(arg))
	}
	if len(group.Watchers) == 0 {
		if rosterUnavailable(sr) {
			return dutystore.Group{}, runtime.NewUserError("%s", groupServiceUnavailableMessage())
		}
		return dutystore.Group{}, runtime.NewUserError("%s", groupWithoutRotationMessage(arg))
	}
	return group, nil
}

// syncChatName renames the current ticket group to reflect the ticket's status,
// level and title, e.g. "[已关闭][P3]Pod 一直 Pending" (closed, priority 3).
//
// It is a no-op for a caller without a Lark group context (REST/MCP/TUI): those
// invocations still update the ticket and return their structured result. A
// rename failure is returned, never swallowed — the ticket has already been
// persisted by the caller, so the user sees "status changed, name did not"
// rather than a silent half-success.
func syncChatName(ctx context.Context, dr *runtime.DynamicRuntime, ticket *model.TicketM) error {
	if ticket == nil || dr == nil || dr.LarkServices == nil || !dr.HasLarkContext() {
		return nil
	}
	name := chatname.FormatName(chatname.StatusForTicket(ticket.Status), ticket.Level, ticket.Title)
	return dr.LarkServices.UpdateChatName(ctx, dr.Address(), name)
}

// callerID is the current caller's identity: the Lark sender for a message
// event, the token's user for a REST/MCP caller. It is empty when neither is
// known, which is also when a name must not be asked for.
func callerID(dr *runtime.DynamicRuntime) string {
	return dr.Identity()
}

// callerName resolves the caller's display name, or "" when there is no Lark
// context or the contact lookup cannot answer. A missing name is a normal
// outcome — it must never turn an otherwise successful command into a failure.
func callerName(ctx context.Context, dr *runtime.DynamicRuntime) string {
	id := callerID(dr)
	if id == "" || dr.LarkServices == nil {
		return ""
	}
	return dr.LarkServices.ResolveUserName(ctx, id)
}

// mentionTag renders a Lark @-mention for a user id.
func mentionTag(userID string) string {
	return fmt.Sprintf("<at user_id=\"%s\"></at>", userID)
}

// markSession persists a mutation to the current conversation's session record
// and mirrors it into dr.SessionM, so a later command in the same turn — and the
// /chat status query — sees the change. It is a no-op when there is no session
// (P2P or a non-Lark caller), which is exactly when the stamp has nowhere to go.
func markSession(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, mutate func(*model.SessionM)) error {
	if dr == nil || dr.SessionM == nil {
		return nil
	}
	mutate(dr.SessionM)
	dr.SessionM.UpdatedAt = time.Now()
	if err := sr.Store.Session().Update(ctx, dr.SessionM); err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}
	return nil
}

// getThisWeekRange returns the date range of the current week (Monday through
// Sunday) as "2026.9.28 ~ 2026.10.4", which the /duty card shows as its header.
func getThisWeekRange() string {
	now := time.Now()
	weekday := int(now.Weekday())
	daysToMonday := weekday - 1
	if weekday == 0 { // time.Sunday == 0; the week starts on Monday here.
		daysToMonday = 6
	}
	monday := now.AddDate(0, 0, -daysToMonday)
	monday = time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, monday.Location())
	sunday := monday.AddDate(0, 0, 6)
	sunday = time.Date(sunday.Year(), sunday.Month(), sunday.Day(), 23, 59, 59, 0, sunday.Location())
	return fmt.Sprintf("%s ~ %s", monday.Format("2006.1.2"), sunday.Format("2006.1.2"))
}
