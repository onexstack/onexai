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

package regen

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"resty.dev/v3"
)

// The admin read surface is deliberately outside duty.Provider: it exists for
// the one-shot import into the native schedule tables, not for the runtime.
// Keeping it here means the importer reuses the same session and wire decoding
// as the provider.

// ScheduleDetail is one Regen schedule with its rotation layers and
// participants.
type ScheduleDetail struct {
	ID                  string
	Name                string
	Description         string
	Timezone            string
	NotificationChannel string
	Layers              []ScheduleLayer
}

// ScheduleLayer is one rotation layer in a ScheduleDetail.
type ScheduleLayer struct {
	ID                   string
	Name                 string
	OrderIndex           int64
	RotationType         string
	RotationStart        string
	ShiftDurationSeconds int64
	Participants         []ScheduleParticipant
}

// ScheduleParticipant is one person in a layer.
type ScheduleParticipant struct {
	UserName   string
	OrderIndex int64
}

// ScheduleOverride is one Regen override.
type ScheduleOverride struct {
	ID           string
	OverrideUser string
	StartTime    string
	EndTime      string
	CreatedBy    string
}

// ScheduleUnavailability is one Regen unavailability.
type ScheduleUnavailability struct {
	ID        string
	UserName  string
	StartDate string
	EndDate   string
	Reason    string
}

type wireScheduleDetail struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	Description         string              `json:"description"`
	Timezone            string              `json:"timezone"`
	NotificationChannel string              `json:"notification_channel"`
	RotationStart       string              `json:"rotation_start"`
	Layers              []wireScheduleLayer `json:"layers"`
}

type wireScheduleLayer struct {
	ID                   string                    `json:"id"`
	Name                 string                    `json:"name"`
	OrderIndex           int64                     `json:"order_index"`
	RotationType         string                    `json:"rotation_type"`
	RotationStart        string                    `json:"rotation_start"`
	ShiftDurationSeconds int64                     `json:"shift_duration_seconds"`
	Participants         []wireScheduleParticipant `json:"participants"`
}

type wireScheduleParticipant struct {
	UserName   string `json:"user_name"`
	OrderIndex int64  `json:"order_index"`
}

type wireScheduleOverride struct {
	ID           string `json:"id"`
	OverrideUser string `json:"override_user"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	CreatedBy    string `json:"created_by"`
}

type wireScheduleUnavailability struct {
	ID        string `json:"id"`
	UserName  string `json:"user_name"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Reason    string `json:"reason"`
}

// NewAdminClient builds a client for the one-shot importer. It returns nil when
// no endpoint is configured.
func NewAdminClient(endpoint, username, password string) *Client {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil
	}
	return &Client{
		baseURL:  strings.TrimRight(endpoint, "/"),
		username: strings.TrimSpace(username),
		password: password,
		client:   resty.New().SetTimeout(30 * time.Second).SetCookieJar(jar),
	}
}

// GetSchedule returns one schedule with its layers and participants.
func (c *Client) GetSchedule(ctx context.Context, scheduleID string) (*ScheduleDetail, error) {
	var out wireScheduleDetail
	path := "/api/v1/schedules/" + url.PathEscape(scheduleID)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	detail := &ScheduleDetail{
		ID:                  out.ID,
		Name:                out.Name,
		Description:         out.Description,
		Timezone:            out.Timezone,
		NotificationChannel: out.NotificationChannel,
		Layers:              make([]ScheduleLayer, 0, len(out.Layers)),
	}
	for _, layer := range out.Layers {
		item := ScheduleLayer{
			ID:                   layer.ID,
			Name:                 layer.Name,
			OrderIndex:           layer.OrderIndex,
			RotationType:         layer.RotationType,
			RotationStart:        layer.RotationStart,
			ShiftDurationSeconds: layer.ShiftDurationSeconds,
			Participants:         make([]ScheduleParticipant, 0, len(layer.Participants)),
		}
		// Some Regen versions put rotation_start on the schedule rather than
		// each layer; fall back to it when a layer omits the field.
		if item.RotationStart == "" {
			item.RotationStart = out.RotationStart
		}
		for _, participant := range layer.Participants {
			item.Participants = append(item.Participants, ScheduleParticipant{
				UserName:   participant.UserName,
				OrderIndex: participant.OrderIndex,
			})
		}
		detail.Layers = append(detail.Layers, item)
	}
	return detail, nil
}

// ListOverrides returns every override on a schedule.
func (c *Client) ListOverrides(ctx context.Context, scheduleID string) ([]ScheduleOverride, error) {
	var out struct {
		Data []wireScheduleOverride `json:"data"`
	}
	path := "/api/v1/schedules/" + url.PathEscape(scheduleID) + "/overrides"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	overrides := make([]ScheduleOverride, 0, len(out.Data))
	for _, item := range out.Data {
		overrides = append(overrides, ScheduleOverride{
			ID:           item.ID,
			OverrideUser: item.OverrideUser,
			StartTime:    item.StartTime,
			EndTime:      item.EndTime,
			CreatedBy:    item.CreatedBy,
		})
	}
	return overrides, nil
}

// ListUnavailabilities returns every unavailability on a schedule. Regen
// answers this endpoint with a bare array.
func (c *Client) ListUnavailabilities(ctx context.Context, scheduleID string) ([]ScheduleUnavailability, error) {
	var out []wireScheduleUnavailability
	path := "/api/v1/schedules/" + url.PathEscape(scheduleID) + "/unavailabilities"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	unavailabilities := make([]ScheduleUnavailability, 0, len(out))
	for _, item := range out {
		unavailabilities = append(unavailabilities, ScheduleUnavailability{
			ID:        item.ID,
			UserName:  item.UserName,
			StartDate: item.StartDate,
			EndDate:   item.EndDate,
			Reason:    item.Reason,
		})
	}
	return unavailabilities, nil
}
