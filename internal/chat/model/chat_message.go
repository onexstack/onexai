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
	"time"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

// TableNameChatMessageM returns the physical table name under the configured
// dialect: chat.chat_message on PostgreSQL, onexai_chat_chat_message on
// MySQL.
func TableNameChatMessageM() string {
	return dbschema.Table(dbschema.Chat, "chat_message")
}

// ChatMessageM stores one archived chat message from a dissolved ticket group.
//
// opsassist kept this on a DBA-provisioned PostgreSQL instance and deliberately
// did NOT register it. onexai owns its schema in code, so it is registered here
// for auto-migration.
type ChatMessageM struct {
	ID         int64  `gorm:"column:id;primaryKey;autoIncrement:true;comment:主键 ID" json:"id"`
	MessageID  string `gorm:"column:message_id;not null;uniqueIndex;comment:飞书消息 ID(全局唯一)" json:"message_id"`
	GroupID    string `gorm:"column:group_id;not null;index;comment:群 ID(lark_chat_id)" json:"group_id"`
	SenderID   string `gorm:"column:sender_id;not null;default:'';comment:发送者身份(user_id，或 cli_ 开头的 app_id)" json:"sender_id"`
	SenderName string `gorm:"column:sender_name;not null;default:'';comment:发送者名称" json:"sender_name"`
	Content    string `gorm:"column:content;type:text;not null;comment:消息内容(文本)" json:"content"`
	// Images is a JSON array of image references. It is separate from Content
	// because the two describe different things: Content is what was said,
	// Images is what was shown, and a message can be either or both.
	Images    string     `gorm:"column:images;type:text;comment:图片引用(JSON)" json:"images"`
	Time      *time.Time `gorm:"column:time;comment:消息时间(秒级精度)" json:"time"`
	Position  string     `gorm:"column:position;not null;default:'';comment:消息在群内位置信息" json:"position"`
	ParentID  string     `gorm:"column:parent_id;not null;default:'';comment:引用/回复的父消息 ID" json:"parent_id"`
	CreatedAt time.Time  `gorm:"column:created_at;not null;default:current_timestamp;comment:创建时间" json:"created_at"`
	UpdatedAt time.Time  `gorm:"column:updated_at;not null;default:current_timestamp;comment:更新时间" json:"updated_at"`
}

// TableName returns the table name.
func (*ChatMessageM) TableName() string {
	return TableNameChatMessageM()
}

func init() {
	registry.Register(&ChatMessageM{})
}
