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
// The original repo for this file is https://github.com/onexstack/onex.

// Package interactionlog persists one record per user↔assistant turn for
// security auditing.
//
// Two properties shape it:
//
//   - It never blocks a turn. An audit write is a side effect of answering, not
//     part of answering, so it runs after the answer is on its way and a
//     failure is logged rather than returned. An audit table that is full must
//     not become an outage.
//   - It masks before it writes. The record is the one place a user's question
//     and the model's answer are stored together, which makes it the place most
//     likely to hold something they would not expect to find in a database.
package interactionlog

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/datamask"
)

// Status values for a recorded turn.
const (
	StatusSuccess = "success"
	StatusError   = "error"
	StatusStopped = "stopped"
	StatusBlocked = "blocked"
)

// Recorder is the narrow write surface this package needs. The store
// implements it; keeping it narrow means a test needs no database and this
// package depends on no store.
type Recorder interface {
	Create(ctx context.Context, interaction *model.InteractionM) error
}

// Entry is one turn to record. It is deliberately the fields an agent run
// actually knows: the chat-only columns of the table (chat type, chat id) are
// filled by the chat service, not here, so they stay empty for a turn that
// arrived over the API.
type Entry struct {
	// Source names the surface the turn arrived on, e.g. "chat" or "api".
	Source string
	// SessionKey is the conversation bucket the turn belongs to.
	SessionKey string
	// SenderID and SenderName identify the human asker. The name is recorded
	// unmasked on purpose: an audit record exists to answer "who asked what".
	SenderID   string
	SenderName string
	// IsSRE is the asker's asserted SRE membership.
	IsSRE bool
	// AgentNames is the Agent selection the turn ran with, as the caller
	// requested it. Empty is a real value — the turn pinned no Agent and ran the
	// engine's built-in assistant — so it is written through as an empty array
	// rather than being skipped.
	AgentNames []string
	// UserInput is the question, masked on write.
	UserInput string
	// FinalAnswer is the answer, masked on write. Empty when the turn produced
	// none.
	FinalAnswer string
	// Status is one of the Status values.
	Status string
	// ErrorMessage explains a non-success status, masked on write.
	ErrorMessage string
	// Duration is how long the turn took.
	Duration time.Duration
}

// Writer records turns. A nil Writer records nothing, which is what a
// deployment with no audit store has.
type Writer struct {
	recorder Recorder
}

// New returns a Writer, or nil when there is nowhere to write.
func New(recorder Recorder) *Writer {
	if recorder == nil {
		return nil
	}
	return &Writer{recorder: recorder}
}

// Enabled reports whether anything will be written. A nil Writer is safe to
// call it on, which spares every call site a nil check.
func (w *Writer) Enabled() bool { return w != nil && w.recorder != nil }

// Write records the turn. It never returns an error: the caller has already
// produced an answer, and failing it over an audit write would be the wrong
// trade every time.
func (w *Writer) Write(ctx context.Context, entry Entry) {
	if !w.Enabled() {
		return
	}
	record := &model.InteractionM{
		InteractionID: uuid.NewString(),
		Source:        entry.Source,
		ChatID:        entry.SessionKey,
		SenderID:      entry.SenderID,
		SenderName:    entry.SenderName,
		IsSRE:         entry.IsSRE,
		AgentNames:    entry.AgentNames,
		UserInput:     datamask.Mask(entry.UserInput),
		FinalAnswer:   datamask.Mask(entry.FinalAnswer),
		ErrorMessage:  datamask.Mask(entry.ErrorMessage),
		Status:        entry.Status,
		TotalMs:       entry.Duration.Milliseconds(),
		CreatedAt:     time.Now(),
	}
	// Detached from the request's cancellation: the write outlives the request,
	// and a context cancelled the moment the answer was delivered would abort
	// the record of that very answer.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := w.recorder.Create(writeCtx, record); err != nil {
		slog.WarnContext(ctx, "interaction audit write failed", "interaction_id", record.InteractionID, "error", err)
	}
}
