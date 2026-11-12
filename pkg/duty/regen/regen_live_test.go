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
	"os"
	"testing"

	"github.com/onexstack/onexai/pkg/duty"
)

// liveClient builds a client against the instance named by
// OAI_REGEN_LIVE_ENDPOINT, or skips the test when it is not set. The account
// comes from OAI_REGEN_LIVE_EMAIL / OAI_REGEN_LIVE_PASSWORD, so no credential
// is ever written down here.
func liveClient(t *testing.T) duty.Provider {
	t.Helper()

	endpoint := os.Getenv("OAI_REGEN_LIVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("set OAI_REGEN_LIVE_ENDPOINT to run the live check")
	}
	cli := NewClient(endpoint, os.Getenv("OAI_REGEN_LIVE_EMAIL"), os.Getenv("OAI_REGEN_LIVE_PASSWORD"))
	if !cli.Configured() {
		t.Fatalf("client not configured for %q", endpoint)
	}
	return cli
}

// TestRegenLiveOnCall reads the schedules and their on-call users, which is
// exactly what the duty store refreshes from. It is the check that the module
// names the ChatOps commands take ("onex", "go", ...) really are the schedule
// names Regen holds.
func TestRegenLiveOnCall(t *testing.T) {
	cli := liveClient(t)
	ctx := context.Background()

	schedules, err := cli.Schedules(ctx)
	if err != nil {
		t.Fatalf("Schedules() = %v", err)
	}
	if len(schedules) == 0 {
		t.Fatal("no schedules on the instance")
	}

	for _, schedule := range schedules {
		oncall, err := cli.OnCall(ctx, schedule.ID)
		if err != nil {
			t.Errorf("OnCall(%s) = %v", schedule.Name, err)
			continue
		}
		t.Logf("%-12s -> %q", schedule.Name, oncall.Name)
	}
}

// TestRegenLiveIncidentRoundTrip exercises the calls the ticket mirror makes.
//
// It leaves an incident behind: Regen has no delete endpoint, and resolved is
// terminal, so every run adds one resolved "wiring check" record. Run it
// against a test instance, not one people watch.
func TestRegenLiveIncidentRoundTrip(t *testing.T) {
	cli := liveClient(t)
	ctx := context.Background()

	// acknowledged → resolved is the path /mark then /close takes. It is also
	// the path that rules out mapping a closed ticket to canceled: Regen
	// rejects that transition once an incident has been acknowledged.
	incident, err := cli.CreateIncident(ctx, duty.CreateIncidentRequest{
		Title:       "oai-chat-apiserver wiring check",
		Severity:    duty.SeverityLow,
		Description: "created by TestRegenLiveIncidentRoundTrip; safe to ignore",
	})
	if err != nil {
		t.Fatalf("CreateIncident() = %v", err)
	}
	if incident.ID == "" || incident.Status != duty.StatusTriggered {
		t.Fatalf("CreateIncident() = %+v, want a triggered incident with an id", incident)
	}
	t.Logf("created %s (%s)", incident.ID, incident.Status)

	acknowledged, err := cli.UpdateIncident(ctx, incident.ID, duty.UpdateIncidentRequest{Status: duty.StatusAcknowledged})
	if err != nil {
		t.Fatalf("UpdateIncident(acknowledged) = %v", err)
	}
	if acknowledged.Status != duty.StatusAcknowledged {
		t.Fatalf("status = %q, want acknowledged", acknowledged.Status)
	}

	resolved, err := cli.UpdateIncident(ctx, incident.ID, duty.UpdateIncidentRequest{Status: duty.StatusResolved})
	if err != nil {
		t.Fatalf("UpdateIncident(resolved) = %v", err)
	}
	if resolved.Status != duty.StatusResolved {
		t.Fatalf("status = %q, want resolved", resolved.Status)
	}

	// And the state the pull side reads back.
	got, err := cli.GetIncident(ctx, incident.ID)
	if err != nil {
		t.Fatalf("GetIncident() = %v", err)
	}
	if got.Status != duty.StatusResolved {
		t.Fatalf("GetIncident() status = %q, want resolved", got.Status)
	}
	t.Logf("%s finished as %s", got.ID, got.Status)
}
