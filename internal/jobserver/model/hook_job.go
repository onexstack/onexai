package model

import (
	"github.com/onexstack/onexstack/pkg/rid"
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"
)

// AfterCreate generates and updates the JobID after the database record is created.
func (m *JobM) AfterCreate(tx *gorm.DB) error {
	m.JobID = rid.NewResourceID("job").New(uint64(m.ID))

	return tx.Save(m).Error
}

func init() {
	registry.Register(&JobM{})
}
