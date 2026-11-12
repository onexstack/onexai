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

package model

import (
	"encoding/json"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

// TableNameOncallGroupM returns the physical table name under the configured
// dialect: chat.oncall_group on PostgreSQL, onexai_chat_oncall_group on
// MySQL.
//
// The table was named oai_ai_module and lived in agent's store; the duty
// commands that actually consume it are chat's, so ownership (and the
// name) moved here.
func TableNameOncallGroupM() string {
	return dbschema.Table(dbschema.Chat, "oncall_group")
}

// OncallGroupM is the metadata row for one on-call group (值班组, duty group).
//
// It carries the group's identity and presentation (name/description/welcome
// message/ordering/allocation policy) and a ScheduleID pointing at the Regen
// schedule that supplies the actual on-call roster. Members are not stored
// here: they are read from Regen by schedule id, with a same-name fallback when
// the id is empty.
type OncallGroupM struct {
	ID                 uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	Name               string `gorm:"uniqueIndex;size:128" json:"name"`
	DisplayName        string `gorm:"size:256" json:"displayName"`
	Status             string `gorm:"size:32" json:"status"`
	ShortDesc          string `gorm:"type:text" json:"shortDesc"`
	LongDesc           string `gorm:"type:text" json:"longDesc"`
	Agents             string `gorm:"type:text" json:"agents"` // JSON array of agent references: "@peer" / "@oncall" / "@default" / a bare name
	ScheduleID         string `gorm:"column:schedule_id;size:64;index" json:"scheduleId"`
	AllocationStrategy string `gorm:"column:allocation_strategy;size:64" json:"allocationStrategy"`
	Owner              string `gorm:"column:owner;size:256" json:"owner"`
	IsSplitByBizLine   string `gorm:"column:is_split_by_biz_line;size:16" json:"isSplitByBizLine"`
	GenerateWeeklyRpt  string `gorm:"column:generate_weekly_rpt;size:16" json:"generateWeeklyRpt"`
	Source             string `gorm:"column:source;size:64" json:"source"`
	WelcomeMessage     string `gorm:"column:welcome_message;type:text" json:"welcomeMessage"`
	Namespace          string `gorm:"column:namespace;size:128;index" json:"namespace"`
	Order              int    `json:"order"`
	CreatedAt          int64  `json:"createdAt"`
	UpdatedAt          int64  `json:"updatedAt"`
}

// TableName returns the physical table name.
func (*OncallGroupM) TableName() string {
	return TableNameOncallGroupM()
}

// AgentList decodes the Agents JSON column into a slice of **agent references**.
//
// They are the same entries a conversation's refs hold, and the scheduler runs
// them through the same pipeline — see internal/chat/pkg/agentref. `["@peer",
// "agent_a"]` under the `first` allocator is the shape on-call actually wants:
// the person on duty runs their own agent if they have one, and `agent_a`
// otherwise. An internal employee has no peer binding, so `@peer` resolves to
// nothing for them and the list falls through to `agent_a`.
//
// A malformed or empty value yields nil rather than an error: a group with no
// agents is a valid state (it simply resolves no agents), and failing the whole
// duty refresh over one bad cell would take every group down with it. That
// swallow is safe **reading**; the write path refuses the same value, because
// there the operator is still around to be told — see
// validation.ValidateOncallGroup.
func (m *OncallGroupM) AgentList() []string {
	if m.Agents == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(m.Agents), &out); err != nil {
		return nil
	}
	return out
}

func init() {
	registry.Register(&OncallGroupM{})
}
