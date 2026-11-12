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

// Package chatname renders and parses ticket group chat names in the
// "[status][level]title" form (e.g. "[处理中][P4]Pod 一直 Pending" — in progress, priority 4). The name is
// how a ticket group advertises its state in the Lark conversation list, so the
// status and level commands keep it in step with the ticket.
package chatname

import (
	"fmt"
	"strings"

	"github.com/onexstack/onexai/internal/chat/model"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// Format is the ticket group name layout: [status][level]title.
const Format = chat.ChatNameFormat

// Parts holds the parsed components of a ticket group chat name.
type Parts struct {
	Status string
	Level  string
	Title  string
}

// Parse splits a raw chat name into its status, level and title. The bool is
// false when the name is not in the ticket format — a group the bot was added
// to but did not create — in which case the caller should leave the name alone.
func Parse(raw string) (Parts, bool) {
	var parts []string
	remaining := raw
	for range 2 {
		start := strings.Index(remaining, "[")
		end := strings.Index(remaining, "]")
		if start != 0 || end <= start {
			return Parts{}, false
		}
		parts = append(parts, remaining[start+1:end])
		remaining = remaining[end+1:]
	}
	return Parts{
		Status: parts[0],
		Level:  parts[1],
		Title:  strings.TrimSpace(remaining),
	}, true
}

// FormatName builds a ticket group name from its components.
func FormatName(status string, level int64, title string) string {
	return fmt.Sprintf(Format, status, LevelString(level), title)
}

// LevelString renders a numeric ticket level as "P1".."P4". An out-of-range
// level degrades to P4, which is the lowest priority rather than an empty tag.
func LevelString(level int64) string {
	switch level {
	case 1:
		return "P1"
	case 2:
		return "P2"
	case 3:
		return "P3"
	default:
		return "P4"
	}
}

// StatusForTicket maps a ticket status to the Chinese label shown in the group
// name. resolved shares the 已关闭 (closed) label with closed: from the conversation list's point
// of view both mean "no longer being worked".
func StatusForTicket(status model.TicketStatus) string {
	switch status {
	case model.TicketStatusSaved:
		return chat.StatusSaved
	case model.TicketStatusClosed, model.TicketStatusResolved:
		return chat.StatusClosed
	default:
		return chat.StatusProcessing
	}
}

// Avatar image keys. These are tenant-specific uploads, so they are empty until
// configured for this deployment; callers skip the avatar update when the key
// is empty rather than sending a blank one.
const (
	avatarKeyRed   = ""
	avatarKeyBlue  = ""
	avatarKeyGreen = ""
)

// AvatarKeyForLevel returns the image key for a ticket level (1-2 red, else
// blue), or "" when no avatar is configured for this tenant.
func AvatarKeyForLevel(level int64) string {
	if level <= 2 {
		return avatarKeyRed
	}
	return avatarKeyBlue
}

// AvatarKeyResolved returns the image key for a closed ticket, or "" when none
// is configured.
func AvatarKeyResolved() string { return avatarKeyGreen }
