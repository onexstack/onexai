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

// Lark card template IDs used by the SRE duty bot. These identify the card
// templates rendered for duty, ticket, and help interactions.
const (
	// Hardcoded template IDs (consider moving to config)
	// DutyGroupCardFromP2P   = "AAqhL1BwtU4Sv"
	DutyGroupCardFromP2P           = "AAqXXi2F8aks9"
	OncallFromP2P                  = "AAqXGV3vKosfg"
	DutyGroupCardFromChat          = "AAqhbswll43Kd"
	DutyListCardTemplateID         = "AAqhL0ofTdS0R"
	ChatInfoTemplateID             = "AAqXqTJKI9SQ6"
	HelpTemplateID                 = "AAqXVPGVTmWpR"
	OnCallCardTemplateID           = "AAqvPkGB222ac"
	OncallWithChildTemplateID      = "AAqvl0VeHZJRY" // deprecated: no longer used after flat group dropdown refactor
	OncallFlatSelectCardID         = "AAqhL2IO1P5p5"
	SREHelpCard                    = "AAqhe4afUrZmY"
	SRECompleteCreateTicketCard    = "AAqhpwys6rmia"
	SREP2PCompleteCreateTicketCard = "AAqXy22J9DoCQ"
	SRECancelCreateTicketCard      = "AAqX6zHRN8HwY"
	SREResolvedCard                = "AAqhpV6mp7X6K"
	SRECloseCard                   = "AAqXyFYlLYs9H"
	SRESavedCard                   = "AAqX6egSgIipD"
	SREHandOverCard                = "AAqhpUBgxE6Fq"
	SREEnterChatCard               = "AAqhpSpXNR5zc"
	SREGetPodCard                  = "AAqXrG6CFDrGj"
	SREKVTableCard                 = "AAqXDCDk6MGsH"
	SREPodInfoListCard             = "AAqvPkpDiokKE"
	// SREEC2InfoListCard             = "AAqX4v8FdeFdU"
	SREEC2InfoListCard      = "AAq2JDfVQ89Mf"
	SREBitTableCard         = "AAqXd0bYXTF7R"
	SREOpsNotifyCard        = "AAq2GHEsdWE3b"
	SREOpsNotifySuccessCard = "AAq2nSDQfyK85"
)

const (
	// OpsNotifyProcessID is the process_info_id for ops notify workflow.
	OpsNotifyProcessID = 3
)

// CardTemplateTitles names each template above, for the surfaces that have to
// describe a card they cannot read.
//
// A template card is rendered by Feishu, not by us: the message carries the
// template id and the variables the producer filled in, and nothing else —
// there is no API that hands the template's own copy back. So when a user
// replies to a duty or ticket card, the most that can be said about it is what
// it is and what went into it, which is what this table is for.
//
// Keyed by template id, so a rename here cannot drift from the constant it
// names. A card whose id is absent is reported by its raw id.
var CardTemplateTitles = map[string]string{
	DutyGroupCardFromP2P:           "值班组卡片（私聊）",
	DutyGroupCardFromChat:          "值班组卡片（群聊）",
	DutyListCardTemplateID:         "值班列表卡片",
	OncallFromP2P:                  "值班卡片（私聊）",
	OnCallCardTemplateID:           "值班卡片",
	OncallFlatSelectCardID:         "值班组选择卡片",
	ChatInfoTemplateID:             "群信息卡片",
	HelpTemplateID:                 "帮助卡片",
	SREHelpCard:                    "SRE 帮助卡片",
	SRECompleteCreateTicketCard:    "建单完成卡片",
	SREP2PCompleteCreateTicketCard: "建单完成卡片（私聊）",
	SRECancelCreateTicketCard:      "取消建单卡片",
	SREResolvedCard:                "工单已解决卡片",
	SRECloseCard:                   "工单已关闭卡片",
	SRESavedCard:                   "工单已保存卡片",
	SREHandOverCard:                "工单转交卡片",
	SREEnterChatCard:               "入群卡片",
	SREGetPodCard:                  "Pod 查询卡片",
	SREKVTableCard:                 "K/V 表格卡片",
	SREPodInfoListCard:             "Pod 列表卡片",
	SREEC2InfoListCard:             "EC2 列表卡片",
	SREBitTableCard:                "Bit 表格卡片",
	SREOpsNotifyCard:               "运维通知卡片",
	SREOpsNotifySuccessCard:        "运维通知成功卡片",
}

// CardTemplateTitle returns the display name of a card template, or "" when the
// id is one this service does not send.
func CardTemplateTitle(templateID string) string {
	return CardTemplateTitles[templateID]
}
