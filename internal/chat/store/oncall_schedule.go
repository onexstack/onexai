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

package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/onexstack/onexai/internal/chat/model"
)

// OncallScheduleStore is the persistence API for a group's native schedule.
//
// A schedule is 1:1 with an on-call group and keyed by the group name. Reads
// return the aggregate the evaluator needs; writes are grouped by intent so
// the biz layer never has to know which child table a field lives in.
type OncallScheduleStore interface {
	Create(ctx context.Context, obj *model.OncallScheduleM) error
	Update(ctx context.Context, obj *model.OncallScheduleM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.OncallScheduleM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.OncallScheduleM, error)

	OncallScheduleExpansion
}

// OncallScheduleExpansion carries the aggregate and operational operations.
type OncallScheduleExpansion interface {
	// GetAggregate returns the whole schedule for a group, or (nil, nil) when
	// the group has no native schedule.
	GetAggregate(ctx context.Context, group string) (*model.OncallScheduleAggregate, error)
	// ListAggregates returns every configured native schedule.
	ListAggregates(ctx context.Context) ([]*model.OncallScheduleAggregate, error)
	// SaveDefinition replaces the rotation definition in one transaction.
	// Overrides, unavailabilities, holidays and escalation tiers are preserved.
	SaveDefinition(ctx context.Context, def *model.OncallScheduleDefinition) error
	// SaveEscalation replaces the policy toggles and every tier in one
	// transaction.
	SaveEscalation(ctx context.Context, schedule *model.OncallScheduleM, tiers []*model.OncallEscalationTierM) error
	// DeleteForGroup removes the schedule and every child row.
	DeleteForGroup(ctx context.Context, group string) error

	ListOverrides(ctx context.Context, group string) ([]*model.OncallScheduleOverrideM, error)
	GetOverride(ctx context.Context, group, id string) (*model.OncallScheduleOverrideM, error)
	CreateOverride(ctx context.Context, obj *model.OncallScheduleOverrideM) error
	DeleteOverride(ctx context.Context, group, id string) error

	ListUnavailabilities(ctx context.Context, group string) ([]*model.OncallScheduleUnavailabilityM, error)
	CreateUnavailability(ctx context.Context, obj *model.OncallScheduleUnavailabilityM) error
	DeleteUnavailability(ctx context.Context, group, id string) error

	ListHolidays(ctx context.Context, group string) ([]*model.OncallScheduleHolidayM, error)
	CreateHoliday(ctx context.Context, obj *model.OncallScheduleHolidayM) error
	DeleteHoliday(ctx context.Context, group, id string) error

	ListTiers(ctx context.Context, group string) ([]*model.OncallEscalationTierM, error)
}

type oncallScheduleStore struct {
	*nilSafeStore[model.OncallScheduleM]
	st *store
	db *gorm.DB
}

var _ OncallScheduleStore = (*oncallScheduleStore)(nil)

func newOncallScheduleStore(s *store) *oncallScheduleStore {
	return &oncallScheduleStore{
		nilSafeStore: newNilSafeStore[model.OncallScheduleM](s),
		st:           s,
		db:           s.db,
	}
}

// conn returns the context's transaction when there is one. Every child-table
// write goes through it so multi-table operations are atomic.
func (s *oncallScheduleStore) conn(ctx context.Context) *gorm.DB {
	return s.st.DB(ctx)
}

func (s *oncallScheduleStore) GetAggregate(ctx context.Context, group string) (*model.OncallScheduleAggregate, error) {
	if s.db == nil || group == "" {
		return nil, nil
	}
	var schedule model.OncallScheduleM
	err := s.db.WithContext(ctx).Where("group_name = ?", group).First(&schedule).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	aggs, err := s.loadAggregates(ctx, []*model.OncallScheduleM{&schedule})
	if err != nil {
		return nil, err
	}
	if len(aggs) == 0 {
		return nil, nil
	}
	return aggs[0], nil
}

func (s *oncallScheduleStore) ListAggregates(ctx context.Context) ([]*model.OncallScheduleAggregate, error) {
	if s.db == nil {
		return nil, nil
	}
	var schedules []*model.OncallScheduleM
	if err := s.db.WithContext(ctx).Order("group_name asc").Find(&schedules).Error; err != nil {
		return nil, err
	}
	return s.loadAggregates(ctx, schedules)
}

// loadAggregates batch-loads every child table for the given schedules. One
// query per table keeps the duty refresh at O(1) round trips per refresh rather
// than O(groups).
func (s *oncallScheduleStore) loadAggregates(
	ctx context.Context, schedules []*model.OncallScheduleM,
) ([]*model.OncallScheduleAggregate, error) {
	if len(schedules) == 0 {
		return nil, nil
	}

	groups := make([]string, 0, len(schedules))
	byGroup := make(map[string]*model.OncallScheduleAggregate, len(schedules))
	for _, schedule := range schedules {
		if schedule == nil {
			continue
		}
		groups = append(groups, schedule.GroupName)
		byGroup[schedule.GroupName] = &model.OncallScheduleAggregate{
			Schedule: schedule,
			Layers:   []model.OncallScheduleLayerAggregate{},
		}
	}
	if len(groups) == 0 {
		return nil, nil
	}

	var layers []*model.OncallScheduleLayerM
	if err := s.db.WithContext(ctx).
		Where("group_name IN ?", groups).
		Order("group_name asc, order_index asc").
		Find(&layers).Error; err != nil {
		return nil, err
	}

	layerIDs := make([]uint64, 0, len(layers))
	// The index is (aggregate, position) rather than a pointer into the slice:
	// appending a later layer can reallocate the slice, and a pointer taken
	// before that would silently write participants into the old array.
	type layerSlot struct {
		agg *model.OncallScheduleAggregate
		idx int
	}
	layerIndex := make(map[uint64]layerSlot, len(layers))
	for _, layer := range layers {
		if layer == nil {
			continue
		}
		agg := byGroup[layer.GroupName]
		if agg == nil {
			continue
		}
		agg.Layers = append(agg.Layers, model.OncallScheduleLayerAggregate{Layer: *layer})
		layerIDs = append(layerIDs, layer.ID)
		layerIndex[layer.ID] = layerSlot{agg: agg, idx: len(agg.Layers) - 1}
	}

	if len(layerIDs) > 0 {
		var participants []*model.OncallScheduleParticipantM
		if err := s.db.WithContext(ctx).
			Where("layer_id IN ?", layerIDs).
			Order("layer_id asc, order_index asc").
			Find(&participants).Error; err != nil {
			return nil, err
		}
		for _, participant := range participants {
			if participant == nil {
				continue
			}
			if slot, ok := layerIndex[participant.LayerID]; ok {
				slot.agg.Layers[slot.idx].Participants = append(
					slot.agg.Layers[slot.idx].Participants, participant)
			}
		}
	}

	var overrides []*model.OncallScheduleOverrideM
	if err := s.db.WithContext(ctx).
		Where("group_name IN ?", groups).
		Order("start_time asc").
		Find(&overrides).Error; err != nil {
		return nil, err
	}
	for _, override := range overrides {
		if agg := byGroup[override.GroupName]; agg != nil {
			agg.Overrides = append(agg.Overrides, override)
		}
	}

	var unavailabilities []*model.OncallScheduleUnavailabilityM
	if err := s.db.WithContext(ctx).
		Where("group_name IN ?", groups).
		Order("start_date asc").
		Find(&unavailabilities).Error; err != nil {
		return nil, err
	}
	for _, unavailability := range unavailabilities {
		if agg := byGroup[unavailability.GroupName]; agg != nil {
			agg.Unavailabilities = append(agg.Unavailabilities, unavailability)
		}
	}

	var holidays []*model.OncallScheduleHolidayM
	if err := s.db.WithContext(ctx).
		Where("group_name IN ?", groups).
		Order("date asc").
		Find(&holidays).Error; err != nil {
		return nil, err
	}
	for _, holiday := range holidays {
		if agg := byGroup[holiday.GroupName]; agg != nil {
			agg.Holidays = append(agg.Holidays, holiday)
		}
	}

	var tiers []*model.OncallEscalationTierM
	if err := s.db.WithContext(ctx).
		Where("group_name IN ?", groups).
		Order("group_name asc, tier_index asc").
		Find(&tiers).Error; err != nil {
		return nil, err
	}
	for _, tier := range tiers {
		if agg := byGroup[tier.GroupName]; agg != nil {
			agg.Tiers = append(agg.Tiers, tier)
		}
	}

	out := make([]*model.OncallScheduleAggregate, 0, len(groups))
	for _, schedule := range schedules {
		if schedule == nil {
			continue
		}
		if agg := byGroup[schedule.GroupName]; agg != nil {
			out = append(out, agg)
		}
	}
	return out, nil
}

func (s *oncallScheduleStore) SaveDefinition(ctx context.Context, def *model.OncallScheduleDefinition) error {
	if s.db == nil || def == nil || def.Schedule == nil || def.Schedule.GroupName == "" {
		return nil
	}
	now := time.Now().UnixMilli()
	def.Schedule.UpdatedAt = now
	if def.Schedule.CreatedAt == 0 {
		def.Schedule.CreatedAt = now
	}

	return s.st.TX(ctx, func(ctx context.Context) error {
		db := s.conn(ctx)

		// The header is an upsert, but the escalation columns are deliberately
		// not in the update list: the schedule endpoint edits rotation only, and
		// the escalation endpoint is the sole writer of those fields.
		if err := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "group_name"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"timezone", "description", "notification_channel", "updated_at",
			}),
		}).Create(def.Schedule).Error; err != nil {
			return err
		}

		var oldLayerIDs []uint64
		if err := db.Model(&model.OncallScheduleLayerM{}).
			Where("group_name = ?", def.Schedule.GroupName).
			Pluck("id", &oldLayerIDs).Error; err != nil {
			return err
		}
		if len(oldLayerIDs) > 0 {
			if err := db.Where("layer_id IN ?", oldLayerIDs).
				Delete(&model.OncallScheduleParticipantM{}).Error; err != nil {
				return err
			}
		}
		if err := db.Where("group_name = ?", def.Schedule.GroupName).
			Delete(&model.OncallScheduleLayerM{}).Error; err != nil {
			return err
		}

		for i, layer := range def.Layers {
			if layer == nil {
				continue
			}
			layer.ID = 0
			layer.GroupName = def.Schedule.GroupName
			layer.CreatedAt = now
			if err := db.Create(layer).Error; err != nil {
				return err
			}
			for _, participant := range def.Participants[i] {
				if participant == nil {
					continue
				}
				participant.ID = 0
				participant.LayerID = layer.ID
				participant.CreatedAt = now
				if err := db.Create(participant).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *oncallScheduleStore) SaveEscalation(
	ctx context.Context, schedule *model.OncallScheduleM, tiers []*model.OncallEscalationTierM,
) error {
	if s.db == nil || schedule == nil || schedule.GroupName == "" {
		return nil
	}
	now := time.Now().UnixMilli()

	return s.st.TX(ctx, func(ctx context.Context) error {
		db := s.conn(ctx)
		var existing model.OncallScheduleM
		if err := db.Where("group_name = ?", schedule.GroupName).First(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRecordNotFound
			}
			return err
		}
		if err := db.Model(&model.OncallScheduleM{}).
			Where("group_name = ?", schedule.GroupName).
			Updates(map[string]any{
				"escalation_enabled":    schedule.EscalationEnabled,
				"escalation_auto":       schedule.EscalationAuto,
				"escalation_from_level": schedule.EscalationFromLevel,
				"updated_at":            now,
			}).Error; err != nil {
			return err
		}

		if err := db.Where("group_name = ?", schedule.GroupName).
			Delete(&model.OncallEscalationTierM{}).Error; err != nil {
			return err
		}
		for _, tier := range tiers {
			if tier == nil {
				continue
			}
			tier.ID = 0
			tier.GroupName = schedule.GroupName
			tier.CreatedAt = now
			if err := db.Create(tier).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *oncallScheduleStore) DeleteForGroup(ctx context.Context, group string) error {
	if s.db == nil || group == "" {
		return nil
	}
	return s.st.TX(ctx, func(ctx context.Context) error {
		db := s.conn(ctx)
		var layerIDs []uint64
		if err := db.Model(&model.OncallScheduleLayerM{}).
			Where("group_name = ?", group).
			Pluck("id", &layerIDs).Error; err != nil {
			return err
		}
		if len(layerIDs) > 0 {
			if err := db.Where("layer_id IN ?", layerIDs).
				Delete(&model.OncallScheduleParticipantM{}).Error; err != nil {
				return err
			}
		}
		for _, child := range []any{
			&model.OncallScheduleLayerM{},
			&model.OncallScheduleOverrideM{},
			&model.OncallScheduleUnavailabilityM{},
			&model.OncallScheduleHolidayM{},
			&model.OncallEscalationTierM{},
		} {
			if err := db.Where("group_name = ?", group).Delete(child).Error; err != nil {
				return err
			}
		}
		return db.Where("group_name = ?", group).Delete(&model.OncallScheduleM{}).Error
	})
}

func (s *oncallScheduleStore) ListOverrides(
	ctx context.Context, group string,
) ([]*model.OncallScheduleOverrideM, error) {
	if s.db == nil {
		return nil, nil
	}
	var rows []*model.OncallScheduleOverrideM
	err := s.db.WithContext(ctx).
		Where("group_name = ?", group).
		Order("start_time asc").
		Find(&rows).Error
	return rows, err
}

func (s *oncallScheduleStore) GetOverride(
	ctx context.Context, group, id string,
) (*model.OncallScheduleOverrideM, error) {
	if s.db == nil {
		return nil, ErrRecordNotFound
	}
	var row model.OncallScheduleOverrideM
	err := s.db.WithContext(ctx).
		Where("group_name = ? AND id = ?", group, id).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRecordNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *oncallScheduleStore) CreateOverride(ctx context.Context, obj *model.OncallScheduleOverrideM) error {
	if s.db == nil || obj == nil {
		return nil
	}
	if obj.ID == "" {
		obj.ID = uuid.NewString()
	}
	if obj.CreatedAt == 0 {
		obj.CreatedAt = time.Now().UnixMilli()
	}
	return s.db.WithContext(ctx).Create(obj).Error
}

func (s *oncallScheduleStore) DeleteOverride(ctx context.Context, group, id string) error {
	if s.db == nil {
		return ErrRecordNotFound
	}
	result := s.db.WithContext(ctx).
		Where("group_name = ? AND id = ?", group, id).
		Delete(&model.OncallScheduleOverrideM{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrRecordNotFound
	}
	return nil
}

func (s *oncallScheduleStore) ListUnavailabilities(
	ctx context.Context, group string,
) ([]*model.OncallScheduleUnavailabilityM, error) {
	if s.db == nil {
		return nil, nil
	}
	var rows []*model.OncallScheduleUnavailabilityM
	err := s.db.WithContext(ctx).
		Where("group_name = ?", group).
		Order("start_date asc").
		Find(&rows).Error
	return rows, err
}

func (s *oncallScheduleStore) CreateUnavailability(
	ctx context.Context, obj *model.OncallScheduleUnavailabilityM,
) error {
	if s.db == nil || obj == nil {
		return nil
	}
	if obj.ID == "" {
		obj.ID = uuid.NewString()
	}
	if obj.CreatedAt == 0 {
		obj.CreatedAt = time.Now().UnixMilli()
	}
	return s.db.WithContext(ctx).Create(obj).Error
}

func (s *oncallScheduleStore) DeleteUnavailability(ctx context.Context, group, id string) error {
	if s.db == nil {
		return ErrRecordNotFound
	}
	result := s.db.WithContext(ctx).
		Where("group_name = ? AND id = ?", group, id).
		Delete(&model.OncallScheduleUnavailabilityM{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrRecordNotFound
	}
	return nil
}

func (s *oncallScheduleStore) ListHolidays(
	ctx context.Context, group string,
) ([]*model.OncallScheduleHolidayM, error) {
	if s.db == nil {
		return nil, nil
	}
	var rows []*model.OncallScheduleHolidayM
	err := s.db.WithContext(ctx).
		Where("group_name = ?", group).
		Order("date asc").
		Find(&rows).Error
	return rows, err
}

func (s *oncallScheduleStore) CreateHoliday(ctx context.Context, obj *model.OncallScheduleHolidayM) error {
	if s.db == nil || obj == nil {
		return nil
	}
	if obj.ID == "" {
		obj.ID = uuid.NewString()
	}
	if obj.CreatedAt == 0 {
		obj.CreatedAt = time.Now().UnixMilli()
	}
	return s.db.WithContext(ctx).Create(obj).Error
}

func (s *oncallScheduleStore) DeleteHoliday(ctx context.Context, group, id string) error {
	if s.db == nil {
		return ErrRecordNotFound
	}
	result := s.db.WithContext(ctx).
		Where("group_name = ? AND id = ?", group, id).
		Delete(&model.OncallScheduleHolidayM{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrRecordNotFound
	}
	return nil
}

func (s *oncallScheduleStore) ListTiers(
	ctx context.Context, group string,
) ([]*model.OncallEscalationTierM, error) {
	if s.db == nil {
		return nil, nil
	}
	var rows []*model.OncallEscalationTierM
	err := s.db.WithContext(ctx).
		Where("group_name = ?", group).
		Order("tier_index asc").
		Find(&rows).Error
	return rows, err
}
