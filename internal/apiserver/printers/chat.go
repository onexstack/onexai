// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package printers

import (
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/onexstack/onexai/pkg/apis/chat"
)

var (
	botAppTable = resourceTable(
		func(obj *chat.BotApp) []metav1.Condition { return obj.Status.Conditions },
		col(colAppID, func(obj *chat.BotApp) any { return orNone(obj.Spec.AppID) }),
		col(colBotName, func(obj *chat.BotApp) any { return orNone(obj.Spec.BotName) }),
		col(colEventMode, func(obj *chat.BotApp) any { return orNone(obj.Spec.EventMode) }),
	)

	dutyGroupTable = resourceTable(
		func(obj *chat.DutyGroup) []metav1.Condition { return obj.Status.Conditions },
		col(colAgents, func(obj *chat.DutyGroup) any { return joinNames(obj.Spec.Agents) }),
		col(colDisplayName, func(obj *chat.DutyGroup) any { return orNone(obj.Spec.DisplayName) }),
	)

	peerBindingTable = resourceTable(
		func(obj *chat.PeerBinding) []metav1.Condition { return obj.Status.Conditions },
		col(colCanonical, func(obj *chat.PeerBinding) any { return orNone(obj.Spec.Canonical) }),
		col(colPeerID, func(obj *chat.PeerBinding) any { return orNone(obj.Spec.PeerID) }),
		col(colAgents, func(obj *chat.PeerBinding) any { return joinNames(obj.Spec.Agents) }),
	)

	// A command has no controller: its body is compiled into the chat server and
	// the object is the catalogue entry. A STATUS column derived from conditions
	// would be permanently Unknown, so the entry's own shape is the table.
	commandTable = newConvertor(
		nameColumn[*chat.Command](),
		col(colGroup, func(obj *chat.Command) any { return orNone(obj.Spec.Group) }),
		col(colEnabled, func(obj *chat.Command) any { return strconv.FormatBool(obj.Spec.Enabled) }),
		col(colAliases, func(obj *chat.Command) any { return joinNames(obj.Spec.Aliases) }),
		col(colUsage, func(obj *chat.Command) any { return orNone(obj.Spec.Usage) }),
		ageColumn[*chat.Command](),
	)
)

// BotApps returns the table convertor for botapps.
func BotApps() rest.TableConvertor { return botAppTable }

// Commands returns the table convertor for commands.
func Commands() rest.TableConvertor { return commandTable }

// DutyGroups returns the table convertor for dutygroups.
func DutyGroups() rest.TableConvertor { return dutyGroupTable }

// PeerBindings returns the table convertor for peerbindings.
func PeerBindings() rest.TableConvertor { return peerBindingTable }
