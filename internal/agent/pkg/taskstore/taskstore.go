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

// Package taskstore is the durable task contract a remote A2A runtime writes
// through. It is a projection over the run store: one task is one run row and
// its version is the optimistic concurrency token.
package taskstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
)

// Task is one stored A2A task.
type Task struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	State     string `json:"state"`
	Version   int64  `json:"version"`
	TaskJSON  string `json:"taskJson"`
}

// Runs is the narrow run-store surface the task contract needs.
type Runs interface {
	CreateRun(ctx context.Context, run *model.RunM) error
	GetRun(ctx context.Context, id string) (*model.RunM, error)
	UpdateRunTask(ctx context.Context, runID, taskJSON string, expectedVersion int64) (int64, error)
	ListRun(ctx context.Context, sessionID string, limit int) ([]*model.RunM, error)
	SettleRun(ctx context.Context, run *model.RunM, expectedVersion int64, events []*model.RunEventM) error
}

// Service implements the task contract over the run store.
type Service struct {
	Runs Runs
}

// Create stores a new task.
func (s *Service) Create(ctx context.Context, sessionID, taskJSON string) (*Task, error) {
	if s.Runs == nil {
		return nil, fmt.Errorf("task store is not configured")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	var payload struct {
		ID     string `json:"id"`
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	}
	_ = json.Unmarshal([]byte(taskJSON), &payload)
	taskID := payload.ID
	if taskID == "" {
		taskID = uuid.NewString()
	}
	// Create is idempotent on the task id.
	//
	// A runtime that loses the response to its create retries, and the retry
	// must return the task it already created rather than a conflict: an error
	// there is indistinguishable from "the task was never stored", so the
	// runtime would either give up on a live task or create a second one.
	// A task id that belongs to another conversation is still a conflict — that
	// is not a retry, it is a mistake.
	if existing, err := s.Runs.GetRun(ctx, taskID); err == nil && existing != nil {
		return s.existingTask(existing, sessionID)
	}

	now := time.Now().UnixMilli()
	run := &model.RunM{
		ID:        taskID,
		SessionID: sessionID,
		State:     model.RunStateRunning,
		TaskID:    taskID,
		TaskJSON:  taskJSON,
		StartedAt: now,
		CreatedAt: now,
	}
	if err := s.Runs.CreateRun(ctx, run); err != nil {
		// Lost the race with another create of the same id: the winner's row is
		// the truth, so answer as if this call had created it.
		if existing, readErr := s.Runs.GetRun(ctx, taskID); readErr == nil && existing != nil {
			return s.existingTask(existing, sessionID)
		}
		return nil, err
	}
	return &Task{ID: taskID, SessionID: sessionID, State: run.State, Version: run.Version, TaskJSON: taskJSON}, nil
}

// existingTask answers a create for a task that already exists.
func (s *Service) existingTask(existing *model.RunM, sessionID string) (*Task, error) {
	if existing.SessionID != sessionID {
		return nil, fmt.Errorf("task %q belongs to another session", existing.ID)
	}
	return &Task{
		ID:        existing.ID,
		SessionID: existing.SessionID,
		State:     existing.State,
		Version:   existing.Version,
		TaskJSON:  existing.TaskJSON,
	}, nil
}

// Update writes one versioned snapshot.
func (s *Service) Update(ctx context.Context, taskID, taskJSON string, expectedVersion int64) (*Task, error) {
	if s.Runs == nil {
		return nil, fmt.Errorf("task store is not configured")
	}
	version, err := s.Runs.UpdateRunTask(ctx, taskID, taskJSON, expectedVersion)
	if err != nil {
		return nil, err
	}
	task, err := s.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	task.Version = version
	return task, nil
}

// Get returns one task.
func (s *Service) Get(ctx context.Context, taskID string) (*Task, error) {
	run, err := s.Runs.GetRun(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return &Task{ID: run.ID, SessionID: run.SessionID, State: run.State, Version: run.Version, TaskJSON: run.TaskJSON}, nil
}

// List returns a session's tasks.
func (s *Service) List(ctx context.Context, sessionID string) ([]*Task, error) {
	runs, err := s.Runs.ListRun(ctx, sessionID, 0)
	if err != nil {
		return nil, err
	}
	out := make([]*Task, 0, len(runs))
	for _, run := range runs {
		out = append(out, &Task{ID: run.ID, SessionID: run.SessionID, State: run.State, Version: run.Version, TaskJSON: run.TaskJSON})
	}
	return out, nil
}

// Settle publishes the terminal snapshot and its event atomically.
func (s *Service) Settle(ctx context.Context, taskID, taskJSON string, expectedVersion int64) (*Task, error) {
	if s.Runs == nil {
		return nil, fmt.Errorf("task store is not configured")
	}
	run, err := s.Runs.GetRun(ctx, taskID)
	if err != nil {
		return nil, err
	}
	state := taskState(taskJSON)
	run.TaskJSON = taskJSON
	run.State = state
	run.FinishedAt = time.Now().UnixMilli()
	event := &model.RunEventM{
		Type:      "task.settled",
		Payload:   taskJSON,
		CreatedAt: run.FinishedAt,
	}
	if err := s.Runs.SettleRun(ctx, run, expectedVersion, []*model.RunEventM{event}); err != nil {
		return nil, err
	}
	run.Version = expectedVersion + 1
	return &Task{ID: run.ID, SessionID: run.SessionID, State: run.State, Version: run.Version, TaskJSON: taskJSON}, nil
}

func taskState(taskJSON string) string {
	var payload struct {
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(taskJSON), &payload); err != nil {
		return model.RunStateCompleted
	}
	switch payload.Status.State {
	case "TASK_STATE_FAILED", "TASK_STATE_REJECTED":
		return model.RunStateFailed
	case "TASK_STATE_CANCELED":
		return model.RunStateCanceled
	case "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED":
		return model.RunStateInterrupted
	default:
		return model.RunStateCompleted
	}
}

// IsVersionConflict reports the optimistic-lock failure a caller retries.
func IsVersionConflict(err error) bool { return errors.Is(err, runstore.ErrVersionConflict) }
