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

// Command regen-import copies the schedules still referenced by an on-call
// group's legacy schedule_id into the native SQL schedule tables.
//
// It reads the chat service config for the database and Regen credentials:
//
//	hack/regen-import --config ~/.onexai/oai-chat-apiserver.yaml --dry-run
//	hack/regen-import --config ~/.onexai/oai-chat-apiserver.yaml
//	hack/regen-import --config ~/.onexai/oai-chat-apiserver.yaml --group go --force
//
// A group that already has a native schedule is skipped unless --force is set.
// The import is idempotent per group: --force replaces the rotation and the
// operational rows for that group.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/viper"
	"gorm.io/gorm"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/internal/chat/model"
	chatstore "github.com/onexstack/onexai/internal/chat/store"
	regenprovider "github.com/onexstack/onexai/pkg/duty/regen"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

func main() {
	var (
		configPath = flag.String("config", "", "Path to oai-chat-apiserver config (coredb + backend.regen-*) (required)")
		group      = flag.String("group", "", "Import only this group name")
		dryRun     = flag.Bool("dry-run", false, "Print the parsed schedules without writing")
		force      = flag.Bool("force", false, "Replace an existing native schedule")
	)
	flag.Parse()

	if *configPath == "" {
		fatalf("--config is required")
	}
	if err := run(*configPath, *group, *dryRun, *force); err != nil {
		fatalf("%v", err)
	}
}

func run(configPath, onlyGroup string, dryRun, force bool) error {
	cfg, db, err := openConfig(configPath)
	if err != nil {
		return err
	}
	if cfg.Backend == nil || strings.TrimSpace(cfg.Backend.RegenEndpoint) == "" {
		return fmt.Errorf("%s has no backend.regen-endpoint", configPath)
	}
	regen := regenprovider.NewAdminClient(
		cfg.Backend.RegenEndpoint, cfg.Backend.RegenUsername, cfg.Backend.RegenPassword)
	if regen == nil {
		return fmt.Errorf("build the Regen client")
	}

	if !dryRun {
		if err := registry.Migrate(db); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	st := chatstore.NewStore(db)
	ctx := context.Background()

	groups, err := st.OncallGroups().ListAll(ctx)
	if err != nil {
		return fmt.Errorf("list on-call groups: %w", err)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	imported, skipped := 0, 0
	for _, group := range groups {
		if group == nil {
			continue
		}
		if onlyGroup != "" && group.Name != onlyGroup {
			continue
		}
		if strings.TrimSpace(group.ScheduleID) == "" {
			fmt.Fprintf(os.Stderr, "skip %q: no legacy schedule_id\n", group.Name)
			skipped++
			continue
		}
		existing, err := st.OncallSchedules().GetAggregate(ctx, group.Name)
		if err != nil {
			return fmt.Errorf("read %q: %w", group.Name, err)
		}
		if existing != nil && existing.Schedule != nil && !force {
			fmt.Fprintf(os.Stderr, "skip %q: already has a native schedule (use --force to replace)\n", group.Name)
			skipped++
			continue
		}

		detail, err := regen.GetSchedule(ctx, group.ScheduleID)
		if err != nil {
			return fmt.Errorf("read Regen schedule %q for %q: %w", group.ScheduleID, group.Name, err)
		}
		overrides, err := regen.ListOverrides(ctx, group.ScheduleID)
		if err != nil {
			return fmt.Errorf("read Regen overrides for %q: %w", group.Name, err)
		}
		unavailabilities, err := regen.ListUnavailabilities(ctx, group.ScheduleID)
		if err != nil {
			return fmt.Errorf("read Regen unavailabilities for %q: %w", group.Name, err)
		}

		definition, err := buildDefinition(group.Name, detail)
		if err != nil {
			return fmt.Errorf("convert %q: %w", group.Name, err)
		}
		if dryRun {
			printDryRun(group.Name, definition, overrides, unavailabilities)
			imported++
			continue
		}

		if existing != nil && existing.Schedule != nil {
			if err := st.OncallSchedules().DeleteForGroup(ctx, group.Name); err != nil {
				return fmt.Errorf("clear %q: %w", group.Name, err)
			}
		}
		if err := st.OncallSchedules().SaveDefinition(ctx, definition); err != nil {
			return fmt.Errorf("write schedule for %q: %w", group.Name, err)
		}
		for _, override := range overrides {
			start, err := time.Parse(time.RFC3339, override.StartTime)
			if err != nil {
				fmt.Fprintf(os.Stderr, "skip override %s of %q: bad start_time %q\n", override.ID, group.Name, override.StartTime)
				continue
			}
			end, err := time.Parse(time.RFC3339, override.EndTime)
			if err != nil || !end.After(start) {
				fmt.Fprintf(os.Stderr, "skip override %s of %q: bad end_time\n", override.ID, group.Name)
				continue
			}
			_ = st.OncallSchedules().CreateOverride(ctx, &model.OncallScheduleOverrideM{
				GroupName:    group.Name,
				OverrideUser: override.OverrideUser,
				StartTime:    start.UTC(),
				EndTime:      end.UTC(),
				CreatedBy:    "regen-import",
			})
		}
		for _, u := range unavailabilities {
			start, end, ok := dateRange(u.StartDate, u.EndDate)
			if !ok {
				fmt.Fprintf(os.Stderr, "skip unavailability %s of %q: bad dates\n", u.ID, group.Name)
				continue
			}
			_ = st.OncallSchedules().CreateUnavailability(ctx, &model.OncallScheduleUnavailabilityM{
				GroupName: group.Name,
				UserName:  u.UserName,
				StartDate: start,
				EndDate:   end,
				Reason:    u.Reason,
				CreatedBy: "regen-import",
			})
		}
		fmt.Printf("imported %q from Regen schedule %s (%d layers, %d overrides, %d unavailabilities)\n",
			group.Name, group.ScheduleID, len(definition.Layers), len(overrides), len(unavailabilities))
		imported++
	}

	fmt.Printf("regen import: %d imported, %d skipped\n", imported, skipped)
	return nil
}

func buildDefinition(
	group string, detail *regenprovider.ScheduleDetail,
) (*model.OncallScheduleDefinition, error) {
	if detail == nil {
		return nil, fmt.Errorf("empty schedule")
	}
	timezone := strings.TrimSpace(detail.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	def := &model.OncallScheduleDefinition{
		Schedule: &model.OncallScheduleM{
			GroupName:           group,
			Timezone:            timezone,
			Description:         detail.Description,
			NotificationChannel: detail.NotificationChannel,
			CreatedAt:           time.Now().UnixMilli(),
			UpdatedAt:           time.Now().UnixMilli(),
		},
		Layers:       make([]*model.OncallScheduleLayerM, 0, len(detail.Layers)),
		Participants: make(map[int][]*model.OncallScheduleParticipantM, len(detail.Layers)),
	}

	layers := append([]regenprovider.ScheduleLayer(nil), detail.Layers...)
	sort.SliceStable(layers, func(i, j int) bool { return layers[i].OrderIndex < layers[j].OrderIndex })

	for i, layer := range layers {
		start, err := time.Parse(time.RFC3339, layer.RotationStart)
		if err != nil {
			return nil, fmt.Errorf("layer %q has bad rotation_start %q", layer.Name, layer.RotationStart)
		}
		name := strings.TrimSpace(layer.Name)
		if name == "" {
			name = fmt.Sprintf("Layer %d", i+1)
		}
		duration := layer.ShiftDurationSeconds
		rotationType := strings.ToLower(strings.TrimSpace(layer.RotationType))
		if !model.IsRotationType(rotationType) {
			rotationType = rotationTypeForDuration(duration)
		}
		if duration <= 0 {
			duration = defaultDuration(rotationType)
		}
		def.Layers = append(def.Layers, &model.OncallScheduleLayerM{
			GroupName:            group,
			Name:                 name,
			OrderIndex:           int64(i),
			RotationType:         rotationType,
			RotationStart:        start.UTC(),
			ShiftDurationSeconds: duration,
			CreatedAt:            time.Now().UnixMilli(),
		})

		participants := append([]regenprovider.ScheduleParticipant(nil), layer.Participants...)
		sort.SliceStable(participants, func(i, j int) bool {
			return participants[i].OrderIndex < participants[j].OrderIndex
		})
		out := make([]*model.OncallScheduleParticipantM, 0, len(participants))
		for _, participant := range participants {
			name := strings.TrimSpace(participant.UserName)
			if name == "" {
				continue
			}
			out = append(out, &model.OncallScheduleParticipantM{
				UserName:   name,
				OrderIndex: int64(len(out)),
				CreatedAt:  time.Now().UnixMilli(),
			})
		}
		if len(out) == 0 {
			fmt.Fprintf(os.Stderr, "warn %q layer %q has no participants; it will yield nobody\n", group, name)
		}
		def.Participants[i] = out
	}
	return def, nil
}

func rotationTypeForDuration(seconds int64) string {
	switch {
	case seconds == 86400:
		return model.RotationDaily
	case seconds == 604800:
		return model.RotationWeekly
	default:
		return model.RotationCustom
	}
}

func defaultDuration(rotationType string) int64 {
	switch rotationType {
	case model.RotationDaily:
		return 86400
	case model.RotationWeekly:
		return 604800
	default:
		return 604800
	}
}

func dateRange(start, end string) (string, string, bool) {
	start = firstDate(start)
	end = firstDate(end)
	if start == "" || end == "" {
		return "", "", false
	}
	if _, err := time.Parse("2006-01-02", start); err != nil {
		return "", "", false
	}
	if _, err := time.Parse("2006-01-02", end); err != nil {
		return "", "", false
	}
	if end < start {
		return "", "", false
	}
	return start, end, true
}

func firstDate(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 10 {
		return value[:10]
	}
	return ""
}

func printDryRun(
	group string, def *model.OncallScheduleDefinition,
	overrides []regenprovider.ScheduleOverride,
	unavailabilities []regenprovider.ScheduleUnavailability,
) {
	payload := map[string]any{
		"group":            group,
		"schedule":         def.Schedule,
		"layers":           def.Layers,
		"participants":     def.Participants,
		"overrides":        overrides,
		"unavailabilities": unavailabilities,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode %q: %v\n", group, err)
		return
	}
	fmt.Println(string(encoded))
}

type importConfig struct {
	CoreDB  *customoptions.CoreDBOptions  `mapstructure:"coredb"`
	Backend *customoptions.BackendOptions `mapstructure:"backend"`
}

func openConfig(path string) (*importConfig, *gorm.DB, error) {
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg importConfig
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if cfg.CoreDB == nil {
		return nil, nil, fmt.Errorf("%s has no coredb block", path)
	}
	db, err := cfg.CoreDB.NewDB()
	if err != nil {
		return nil, nil, err
	}
	return &cfg, db, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
