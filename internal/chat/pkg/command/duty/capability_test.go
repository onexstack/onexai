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

package duty

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/api"
	dutystore "github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// fakeCapability answers the schedule subcommands without a database. The
// embedded interface supplies the methods a given test never reaches.
type fakeCapability struct {
	dutystore.Service
	schedule  *api.OncallScheduleResponse
	current   *api.ListOncallCurrentResponse
	timeline  *api.OncallTimelineResponse
	overrides *api.OverrideListResponse

	createdOverride       *api.OncallScheduleOverride
	createdUnavailability *api.OncallScheduleUnavailability
	createdHoliday        *api.OncallScheduleHoliday
}

func (f *fakeCapability) GetSchedule(context.Context, string) (*api.OncallScheduleResponse, error) {
	return f.schedule, nil
}

func (f *fakeCapability) ListCurrent(
	context.Context, []string, time.Time,
) (*api.ListOncallCurrentResponse, error) {
	return f.current, nil
}

func (f *fakeCapability) GetTimeline(
	context.Context, string, time.Time, time.Time,
) (*api.OncallTimelineResponse, error) {
	return f.timeline, nil
}

func (f *fakeCapability) ListOverrides(context.Context, string) (*api.OverrideListResponse, error) {
	return f.overrides, nil
}

func (f *fakeCapability) CreateOverride(
	_ context.Context, group, user string, start, end time.Time, createdBy string,
) (*api.OncallScheduleOverride, error) {
	f.createdOverride = &api.OncallScheduleOverride{
		ID: "ov-new", OverrideUser: user, StartTime: start, EndTime: end, CreatedBy: createdBy,
	}
	return f.createdOverride, nil
}

func (f *fakeCapability) CreateUnavailability(
	_ context.Context, group, user, startDate, endDate, reason, createdBy string,
) (*api.OncallScheduleUnavailability, error) {
	f.createdUnavailability = &api.OncallScheduleUnavailability{
		ID: "un-new", UserName: user, StartDate: startDate, EndDate: endDate, Reason: reason,
	}
	return f.createdUnavailability, nil
}

func (f *fakeCapability) CreateHoliday(
	_ context.Context, group, date, name, country string,
) (*api.OncallScheduleHoliday, error) {
	f.createdHoliday = &api.OncallScheduleHoliday{ID: "ho-new", Date: date, Name: name, CountryCode: country}
	return f.createdHoliday, nil
}

func newFakeCapability() *fakeCapability {
	return &fakeCapability{
		schedule: &api.OncallScheduleResponse{
			Group:      "go",
			Configured: true,
			Source:     "native",
			Schedule: &api.OncallSchedule{
				OncallScheduleDefinition: api.OncallScheduleDefinition{Timezone: "UTC"},
			},
		},
		current: &api.ListOncallCurrentResponse{Items: []*api.OncallCurrentResponse{{
			Group:    "go",
			Timezone: "UTC",
			Current: &api.OncallCurrentMember{
				UserName:   "alice@example.com",
				LayerName:  "Primary",
				IsOverride: true,
				Until:      ptrTime(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)),
			},
			Backups: []api.OncallCurrentMember{{UserName: "bob@example.com"}},
		}}},
		timeline: &api.OncallTimelineResponse{
			Group:    "go",
			From:     time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC),
			To:       time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC),
			Timezone: "UTC",
			Segments: []api.OncallSegment{{
				Start:    time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC),
				End:      time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC),
				UserName: "alice@example.com",
			}},
		},
		overrides: &api.OverrideListResponse{Overrides: []api.OncallScheduleOverride{{
			ID: "ov-1", OverrideUser: "carol@example.com",
			StartTime: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
		}}},
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestDutyCurrentRendersLayerAndUntil(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.sr.Duty = newFakeCapability()

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command {
		return NewDutyCmd(env.sr, out)
	}, "current", "go")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if _, ok := res.Raw.(*api.ListOncallCurrentResponse); !ok {
		t.Fatalf("Raw = %#v, want the current response", res.Raw)
	}
	for _, want := range []string{"alice@example.com", "Primary", "代班", "至", "bob@example.com"} {
		if !strings.Contains(res.Markdown.Content, want) {
			t.Errorf("markdown is missing %q:\n%s", want, res.Markdown.Content)
		}
	}
}

func TestDutyTimelineRendersSegments(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.sr.Duty = newFakeCapability()

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command {
		return NewDutyCmd(env.sr, out)
	}, "timeline", "go", "--from", "2026-11-07T00:00:00Z", "--to", "2026-11-08T00:00:00Z")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if _, ok := res.Raw.(*api.OncallTimelineResponse); !ok {
		t.Fatalf("Raw = %#v, want the timeline response", res.Raw)
	}
	if !strings.Contains(res.Markdown.Content, "alice@example.com") {
		t.Errorf("markdown = %q", res.Markdown.Content)
	}
}

func TestDutyScheduleShow(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.sr.Duty = newFakeCapability()

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command {
		return NewDutyCmd(env.sr, out)
	}, "schedule", "go")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	for _, want := range []string{"来源: native", "时区: UTC"} {
		if !strings.Contains(res.Markdown.Content, want) {
			t.Errorf("markdown is missing %q:\n%s", want, res.Markdown.Content)
		}
	}
}

func TestDutyOverrideList(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.sr.Duty = newFakeCapability()

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command {
		return NewDutyCmd(env.sr, out)
	}, "override", "go")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if _, ok := res.Raw.(*api.OverrideListResponse); !ok {
		t.Fatalf("Raw = %#v, want the override list", res.Raw)
	}
	if !strings.Contains(res.Markdown.Content, "carol@example.com") {
		t.Errorf("markdown = %q", res.Markdown.Content)
	}
}

func TestDutyOverrideAddParsesFlags(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	fake := newFakeCapability()
	env.sr.Duty = fake

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command {
		return NewDutyCmd(env.sr, out)
	}, "override", "add", "go",
		"--user", "carol@example.com",
		"--start", "2026-11-09T00:00:00Z",
		"--end", "2026-11-10T00:00:00Z")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if fake.createdOverride == nil {
		t.Fatal("CreateOverride was not called")
	}
	if fake.createdOverride.OverrideUser != "carol@example.com" {
		t.Errorf("OverrideUser = %q", fake.createdOverride.OverrideUser)
	}
	if !strings.Contains(res.Markdown.Content, "carol@example.com") {
		t.Errorf("markdown = %q", res.Markdown.Content)
	}
}

func TestDutyUnavailableAndHolidayAddParseDates(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	fake := newFakeCapability()
	env.sr.Duty = fake
	build := func(out runtime.Responder) *cobra.Command { return NewDutyCmd(env.sr, out) }

	_, err := env.exec(t, build, "unavailable", "add", "go",
		"--user", "alice@example.com",
		"--start-date", "2026-11-10",
		"--end-date", "2026-11-12",
		"--reason", "年假")
	if err != nil {
		t.Fatalf("unavailable add = %v", err)
	}
	if fake.createdUnavailability == nil || fake.createdUnavailability.Reason != "年假" {
		t.Fatalf("unavailability = %+v", fake.createdUnavailability)
	}

	_, err = env.exec(t, build, "holiday", "add", "go",
		"--date", "2026-12-25", "--name", "圣诞节")
	if err != nil {
		t.Fatalf("holiday add = %v", err)
	}
	if fake.createdHoliday == nil || fake.createdHoliday.Name != "圣诞节" {
		t.Fatalf("holiday = %+v", fake.createdHoliday)
	}
}

// TestDutyShowEscapesReservedWord verifies that a group literally named
// "timeline" can still be queried with the explicit `show` subcommand.
func TestDutyShowEscapesReservedWord(t *testing.T) {
	ticket, session := ticketGroup()
	env := newTestEnv(t, ticket, session)
	env.dutyStore.RegisterGroup(dutystore.Group{
		ID: "timeline", Name: "Timeline Group",
	}.OnDutyGroup())
	env.dutyStore.SetOncall("timeline", nil)

	res, err := env.exec(t, func(out runtime.Responder) *cobra.Command {
		return NewDutyCmd(env.sr, out)
	}, "show", "timeline")
	if err != nil {
		t.Fatalf("execute = %v", err)
	}
	if !strings.Contains(res.PlainText, "timeline") && !strings.Contains(res.Markdown.Content, "timeline") {
		t.Fatalf("show timeline did not reach the group: %+v", res)
	}
}
