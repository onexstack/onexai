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

// Package oncall evaluates a native duty schedule.
//
// The evaluation rules are ported from Regen's schedule evaluator so a rotation
// moved over from there keeps answering the same way:
//
//   - an override wins over every layer;
//   - an unavailable participant is skipped inside their slot, walking to the
//     next participant in the same layer, then to the next layer;
//   - a slot is floor((at - rotationStart) / shiftDuration), and shift
//     boundaries are absolute instants — the schedule timezone is used for
//     calendar days (unavailability and holiday dates), not for shift math;
//   - an empty schedule yields "(nobody)" in a timeline and no watcher at a
//     point in time.
//
// Everything here is a pure function of the aggregate, so the same code backs
// the REST timeline, the duty store and the ticket assignment path.
package oncall

import (
	"sort"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/chat/model"
)

// Nobody is the user name a timeline uses for an unstaffed interval.
const Nobody = "(nobody)"

// MaxBackups bounds how many lower layers WatchersAt reports. A schedule can
// have at most ten layers, so this is a defensive ceiling rather than a
// product rule.
const MaxBackups = 10

// Segment is one interval of a timeline.
type Segment struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	UserName   string    `json:"userName"`
	IsOverride bool      `json:"isOverride"`
	LayerName  string    `json:"layerName,omitempty"`
}

// Current is who is on call at one instant.
type Current struct {
	UserName   string     `json:"userName"`
	IsOverride bool       `json:"isOverride"`
	LayerName  string     `json:"layerName,omitempty"`
	Since      time.Time  `json:"since"`
	Until      *time.Time `json:"until,omitempty"`
}

// Location resolves a schedule timezone. An unknown or empty zone falls back to
// UTC, which is the same judgement the storage layer makes for a fresh
// schedule.
func Location(timezone string) *time.Location {
	if strings.TrimSpace(timezone) == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// CurrentAt returns the person on call at at.
//
// A false result means the schedule has nobody at that instant, which is a
// legitimate state (empty layers, before the rotation starts, or everyone
// unavailable). Callers must not treat it as an error.
func CurrentAt(agg *model.OncallScheduleAggregate, at time.Time) (Current, bool) {
	if agg == nil || agg.Schedule == nil {
		return Current{}, false
	}
	at = at.UTC()

	if override, ok := activeOverride(agg.Overrides, at); ok {
		until := override.EndTime.UTC()
		return Current{
			UserName:   override.OverrideUser,
			IsOverride: true,
			Since:      override.StartTime.UTC(),
			Until:      &until,
		}, true
	}

	unavailable := unavailableSet(agg, at)
	for _, layer := range sortedLayers(agg.Layers) {
		if user, since, until, ok := layerAt(layer, at, unavailable); ok {
			return Current{
				UserName:  user,
				LayerName: layer.Layer.Name,
				Since:     since,
				Until:     &until,
			}, true
		}
	}
	return Current{}, false
}

// WatchersAt returns the primary watcher followed by one current user from each
// other layer, skipping duplicates. The order is what the ticket assignment
// strategy consumes: index 0 is the primary, the rest are backups.
func WatchersAt(agg *model.OncallScheduleAggregate, at time.Time) []Current {
	primary, ok := CurrentAt(agg, at)
	if !ok {
		return nil
	}

	out := []Current{primary}
	seen := map[string]struct{}{strings.ToLower(primary.UserName): {}}
	if len(out) >= MaxBackups {
		return out
	}

	unavailable := unavailableSet(agg, at)

	// An override replaces the primary but not the backup structure, so every
	// layer is still considered as a backup in that case.
	for _, layer := range sortedLayers(agg.Layers) {
		if !primary.IsOverride && layer.Layer.Name == primary.LayerName {
			continue
		}
		user, since, until, ok := layerAt(layer, at, unavailable)
		if !ok {
			continue
		}
		key := strings.ToLower(user)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		untilCopy := until
		out = append(out, Current{
			UserName:  user,
			LayerName: layer.Layer.Name,
			Since:     since,
			Until:     &untilCopy,
		})
		if len(out) >= MaxBackups {
			break
		}
	}
	return out
}

// Timeline returns the effective timeline in [from, to). Adjacent segments with
// the same user and the same override flag are merged.
func Timeline(agg *model.OncallScheduleAggregate, from, to time.Time) []Segment {
	if agg == nil || agg.Schedule == nil || !to.After(from) {
		return nil
	}
	from, to = from.UTC(), to.UTC()
	return buildTimeline(agg, from, to)
}

// LayerTimelines evaluates each layer independently (applying unavailabilities
// but not overrides) and also returns the effective timeline. The map key is
// the layer name.
func LayerTimelines(
	agg *model.OncallScheduleAggregate, from, to time.Time,
) (map[string][]Segment, []Segment) {
	if agg == nil || agg.Schedule == nil || !to.After(from) {
		return map[string][]Segment{}, nil
	}
	from, to = from.UTC(), to.UTC()

	out := make(map[string][]Segment, len(agg.Layers))
	for i, layer := range sortedLayers(agg.Layers) {
		name := layer.Layer.Name
		if name == "" {
			name = "layer-" + itoa(i)
		}
		single := &model.OncallScheduleAggregate{
			Schedule:         agg.Schedule,
			Layers:           []model.OncallScheduleLayerAggregate{layer},
			Unavailabilities: agg.Unavailabilities,
		}
		out[name] = buildTimeline(single, from, to)
	}
	return out, buildTimeline(agg, from, to)
}

// NextBoundary returns the earliest change strictly after after. It is used for
// "until" displays and for deciding when to refresh a cached watcher.
func NextBoundary(agg *model.OncallScheduleAggregate, after time.Time) (time.Time, bool) {
	if agg == nil || agg.Schedule == nil {
		return time.Time{}, false
	}
	after = after.UTC()

	var next time.Time
	consider := func(candidate time.Time) {
		candidate = candidate.UTC()
		if !candidate.After(after) {
			return
		}
		if next.IsZero() || candidate.Before(next) {
			next = candidate
		}
	}

	for _, layer := range agg.Layers {
		duration := time.Duration(layer.Layer.ShiftDurationSeconds) * time.Second
		start := layer.Layer.RotationStart.UTC()
		if duration <= 0 || start.IsZero() {
			continue
		}
		if after.Before(start) {
			consider(start)
			continue
		}
		slot := int64(after.Sub(start) / duration)
		consider(start.Add(time.Duration(slot+1) * duration))
	}

	for _, override := range agg.Overrides {
		consider(override.StartTime)
		consider(override.EndTime)
	}

	loc := Location(agg.Schedule.Timezone)
	for _, u := range agg.Unavailabilities {
		if midnight, err := dateMidnight(u.StartDate, loc); err == nil {
			consider(midnight)
		}
		if resume, err := dateMidnight(u.EndDate, loc); err == nil {
			consider(resume.AddDate(0, 0, 1))
		}
	}

	return next, !next.IsZero()
}

func buildTimeline(agg *model.OncallScheduleAggregate, from, to time.Time) []Segment {
	boundaries := timelineBoundaries(agg, from, to)
	if len(boundaries) < 2 {
		return nil
	}

	out := make([]Segment, 0, len(boundaries)-1)
	for i := 0; i+1 < len(boundaries); i++ {
		start, end := boundaries[i], boundaries[i+1]
		if !end.After(start) {
			continue
		}
		mid := start.Add(end.Sub(start) / 2)
		current, ok := CurrentAt(agg, mid)
		user := Nobody
		layer := ""
		isOverride := false
		if ok {
			user = current.UserName
			layer = current.LayerName
			isOverride = current.IsOverride
		}
		if n := len(out); n > 0 &&
			out[n-1].UserName == user && out[n-1].IsOverride == isOverride {
			out[n-1].End = end
			continue
		}
		out = append(out, Segment{
			Start:      start,
			End:        end,
			UserName:   user,
			IsOverride: isOverride,
			LayerName:  layer,
		})
	}
	return out
}

func timelineBoundaries(agg *model.OncallScheduleAggregate, from, to time.Time) []time.Time {
	set := map[int64]struct{}{
		from.UnixNano(): {},
		to.UnixNano():   {},
	}
	add := func(t time.Time) {
		t = t.UTC()
		if t.Before(from) || t.After(to) {
			return
		}
		set[t.UnixNano()] = struct{}{}
	}

	for _, layer := range agg.Layers {
		duration := time.Duration(layer.Layer.ShiftDurationSeconds) * time.Second
		start := layer.Layer.RotationStart.UTC()
		if duration <= 0 || start.IsZero() {
			continue
		}
		if start.After(from) {
			add(start)
		}
		// Walk from the first boundary at or after from. A shift boundary
		// exactly at from is already in the set.
		if from.After(start) {
			slot := int64(from.Sub(start) / duration)
			for t := start.Add(time.Duration(slot) * duration); t.Before(to); t = t.Add(duration) {
				add(t)
			}
		} else {
			for t := start; t.Before(to); t = t.Add(duration) {
				add(t)
			}
		}
	}

	for _, override := range agg.Overrides {
		add(override.StartTime)
		add(override.EndTime)
	}

	loc := Location(agg.Schedule.Timezone)
	for _, u := range agg.Unavailabilities {
		if midnight, err := dateMidnight(u.StartDate, loc); err == nil {
			add(midnight)
		}
		if resume, err := dateMidnight(u.EndDate, loc); err == nil {
			add(resume.AddDate(0, 0, 1))
		}
	}

	out := make([]time.Time, 0, len(set))
	for ns := range set {
		out = append(out, time.Unix(0, ns).UTC())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func activeOverride(overrides []*model.OncallScheduleOverrideM, at time.Time) (*model.OncallScheduleOverrideM, bool) {
	var best *model.OncallScheduleOverrideM
	for _, override := range overrides {
		if override == nil {
			continue
		}
		start, end := override.StartTime.UTC(), override.EndTime.UTC()
		if at.Before(start) || !at.Before(end) {
			continue
		}
		if best == nil || start.After(best.StartTime.UTC()) {
			best = override
		}
	}
	return best, best != nil
}

func unavailableSet(agg *model.OncallScheduleAggregate, at time.Time) map[string]struct{} {
	if len(agg.Unavailabilities) == 0 {
		return nil
	}
	loc := Location(agg.Schedule.Timezone)
	date := at.In(loc).Format("2006-01-02")
	set := make(map[string]struct{}, len(agg.Unavailabilities))
	for _, u := range agg.Unavailabilities {
		if u == nil {
			continue
		}
		if u.StartDate <= date && date <= u.EndDate {
			set[strings.ToLower(u.UserName)] = struct{}{}
		}
	}
	return set
}

func sortedLayers(layers []model.OncallScheduleLayerAggregate) []model.OncallScheduleLayerAggregate {
	out := make([]model.OncallScheduleLayerAggregate, len(layers))
	copy(out, layers)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Layer.OrderIndex == out[j].Layer.OrderIndex {
			return out[i].Layer.Name < out[j].Layer.Name
		}
		return out[i].Layer.OrderIndex < out[j].Layer.OrderIndex
	})
	return out
}

// layerAt evaluates one layer. unavailable is applied only when non-nil; the
// backup pass deliberately passes nil so a backup is the person the layer would
// name on a normal day, not someone who is already unavailable across the whole
// schedule.
func layerAt(
	layer model.OncallScheduleLayerAggregate, at time.Time, unavailable map[string]struct{},
) (string, time.Time, time.Time, bool) {
	start := layer.Layer.RotationStart.UTC()
	duration := time.Duration(layer.Layer.ShiftDurationSeconds) * time.Second
	if start.IsZero() || duration <= 0 || len(layer.Participants) == 0 || at.Before(start) {
		return "", time.Time{}, time.Time{}, false
	}

	elapsed := at.Sub(start)
	slot := int64(elapsed / duration)
	since := start.Add(time.Duration(slot) * duration)
	until := start.Add(time.Duration(slot+1) * duration)

	participants := sortedParticipants(layer.Participants)
	n := int64(len(participants))
	for i := int64(0); i < n; i++ {
		idx := ((slot+i)%n + n) % n
		user := strings.TrimSpace(participants[idx].UserName)
		if user == "" {
			continue
		}
		if _, skip := unavailable[strings.ToLower(user)]; skip {
			continue
		}
		return user, since, until, true
	}
	return "", time.Time{}, time.Time{}, false
}

func sortedParticipants(participants []*model.OncallScheduleParticipantM) []*model.OncallScheduleParticipantM {
	out := make([]*model.OncallScheduleParticipantM, 0, len(participants))
	for _, participant := range participants {
		if participant != nil {
			out = append(out, participant)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].OrderIndex < out[j].OrderIndex
	})
	return out
}

func dateMidnight(date string, loc *time.Location) (time.Time, error) {
	parsed, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return time.Time{}, err
	}
	return parsed, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
