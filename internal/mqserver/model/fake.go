package model

import (
	"time"
)

const TableNameFakeM = "oai_fake"

// FakeM mapped from table <fake>
type FakeM struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`
	FakeID    string    `gorm:"column:fake_id;not null;comment:资源唯一 ID" json:"fake_id"`                                 // Unique resource ID
	CreatedAt time.Time `gorm:"column:created_at;not null;default:current_timestamp;comment:资源创建时间" json:"createdAt"`   // Resource creation time
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:current_timestamp;comment:资源最后修改时间" json:"updatedAt"` // Resource last modified time
}

// TableName FakeM's table name
func (*FakeM) TableName() string {
	return TableNameFakeM
}
