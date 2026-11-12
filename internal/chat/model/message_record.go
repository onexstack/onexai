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

func init() { registry.Register(&MessageM{}) }

// TableNameMessageM returns the physical name of the IM message cache table
// under the configured dialect: chat.message_record on PostgreSQL,
// onexai_chat_message_record on MySQL.
func TableNameMessageM() string {
	return dbschema.Table(dbschema.Chat, "message_record")
}

// MessageM is a locally cached IM message for history display.
type MessageM struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	MsgID       string `gorm:"uniqueIndex;size:256" json:"msg_id"` // IM-system message ID
	ConvID      string `gorm:"index;size:256" json:"conv_id"`
	SenderID    string `gorm:"size:128" json:"sender_id"`
	SenderName  string `gorm:"size:256" json:"sender_name"`
	Content     string `gorm:"type:text" json:"content"`
	ContentType string `gorm:"size:64" json:"content_type"` // "text" | "rich_text" | "card" | "image"
	Raw         string `gorm:"type:text" json:"raw"`        // original IM payload (e.g. image_key)
	// Images is a JSON array of image references.
	//
	// It is a column of its own rather than a reuse of Raw: Raw holds the
	// platform's payload, which is what the /v1/images re-fetch path parses for
	// an image_key, and a message can have both — a post with a picture is
	// words in Content and a reference here.
	Images    string    `gorm:"type:text" json:"images"`
	Backend   string    `gorm:"size:64" json:"backend"`
	IsBot     bool      `json:"is_bot"`
	SendTime  time.Time `gorm:"index" json:"send_time"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName returns the physical table name.
func (*MessageM) TableName() string { return TableNameMessageM() }
