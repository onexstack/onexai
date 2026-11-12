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

// Package duty defines the on-call and incident contract used by the ChatOps
// commands.
//
// A Provider is one backing system: it answers "who is on call for this
// rotation" and mirrors local tickets as remote incidents. Regen is the
// reference implementation in pkg/duty/regen; a deployment without a provider
// uses the Noop implementation and keeps tickets local.
package duty

import (
	"context"
	"errors"
)

// Schedule is an on-call rotation. Its Name is the duty module code.
type Schedule struct {
	ID   string
	Name string
}

// Person is the answer to "who is on call right now". ID is the provider's
// opaque identity; Name is the display value stored in the rotation.
type Person struct {
	ID   string
	Name string
}

// Status is the lifecycle state of an incident.
type Status string

// Incident statuses. They deliberately match the vocabulary the ChatOps
// commands already use.
const (
	StatusTriggered    Status = "triggered"
	StatusAcknowledged Status = "acknowledged"
	StatusResolved     Status = "resolved"
	StatusCanceled     Status = "canceled"
)

// Severity is an incident's urgency, ordered from highest to lowest.
type Severity string

// Incident severities.
const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

// Incident is the provider's view of a ticket mirror.
type Incident struct {
	ID       string
	Status   Status
	Severity Severity
}

// CreateIncidentRequest is the payload for opening a remote incident.
type CreateIncidentRequest struct {
	Title       string
	Description string
	Severity    Severity
}

// UpdateIncidentRequest patches an incident. Empty fields are left unchanged.
type UpdateIncidentRequest struct {
	Status   Status
	Severity Severity
}

// Roster loads on-call rotations and their current watcher.
type Roster interface {
	// Schedules returns every known rotation.
	Schedules(ctx context.Context) ([]Schedule, error)
	// OnCall returns the person on call for a rotation at the current time.
	OnCall(ctx context.Context, scheduleID string) (Person, error)
}

// Incidents mirrors local tickets as remote incidents.
type Incidents interface {
	CreateIncident(ctx context.Context, in CreateIncidentRequest) (Incident, error)
	GetIncident(ctx context.Context, id string) (Incident, error)
	UpdateIncident(ctx context.Context, id string, in UpdateIncidentRequest) (Incident, error)
}

// Provider is one complete duty backend.
type Provider interface {
	// Name identifies the provider; it is stored as the ticket's source.
	Name() string
	// Configured reports whether the provider is usable at all. Callers use it
	// to skip remote work instead of collecting failures.
	Configured() bool
	Roster
	Incidents
}

// TransitionChecker is implemented by providers whose incident state machine
// rejects some status changes. Callers that do not find it assume every
// transition is allowed.
type TransitionChecker interface {
	CanTransition(from, to Status) bool
}

// ErrNotConfigured is returned by the Noop provider.
var ErrNotConfigured = errors.New("duty: no provider is configured")

// ErrTransitionNotAllowed is returned when a provider refuses a status change
// because its state machine cannot reach the target from the current state.
var ErrTransitionNotAllowed = errors.New("duty: incident transition not allowed")

// Noop is a Provider that is never configured. It keeps a deployment without a
// duty backend on the same code path as one whose backend is down.
type Noop struct{ name string }

// NewNoop returns a Noop provider. name defaults to "none".
func NewNoop(name string) Provider {
	if name == "" {
		name = "none"
	}
	return &Noop{name: name}
}

// Name returns the provider name.
func (n *Noop) Name() string { return n.name }

// Configured reports false.
func (n *Noop) Configured() bool { return false }

// Schedules returns ErrNotConfigured.
func (n *Noop) Schedules(context.Context) ([]Schedule, error) { return nil, ErrNotConfigured }

// OnCall returns ErrNotConfigured.
func (n *Noop) OnCall(context.Context, string) (Person, error) { return Person{}, ErrNotConfigured }

// CreateIncident returns ErrNotConfigured.
func (n *Noop) CreateIncident(context.Context, CreateIncidentRequest) (Incident, error) {
	return Incident{}, ErrNotConfigured
}

// GetIncident returns ErrNotConfigured.
func (n *Noop) GetIncident(context.Context, string) (Incident, error) {
	return Incident{}, ErrNotConfigured
}

// UpdateIncident returns ErrNotConfigured.
func (n *Noop) UpdateIncident(context.Context, string, UpdateIncidentRequest) (Incident, error) {
	return Incident{}, ErrNotConfigured
}
