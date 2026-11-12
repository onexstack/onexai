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

// Package agent is the port through which the chat drives an AI agent, and
// the neutral event model its backends emit.
//
// There are two implementations today, and they differ in almost everything
// except this interface. pkg/agent/llm drives oai-agent-apiserver over HTTP/SSE: a run
// is one request, the conversation lives in the server, and there is nothing on
// this host to clean up. pkg/agent/cli drives a local agent process: a run is
// one turn of a conversation whose subprocess and workspace must be kept alive,
// reaped when idle, and answered when it asks for permission.
//
// A caller picks a backend and then talks only to a Session, so which of those
// two it got never shows in the calling code.
package agent

import (
	"context"
	"errors"

	"github.com/onexstack/onexai/pkg/media"
)

// Backend names.
//
// They mirror session.BackendLLM/CLI and options.AgentBackendLLM/CLI. The
// repetition is the same one pkg/session already makes, for the same reason:
// this package is a port, and a port that had to import the configuration
// package to name its own backends would be pointing the dependency arrow the
// wrong way. What keeps the copies from drifting is that the values travel
// between the packages at runtime — a backend that did not match would simply
// never resolve.
const (
	// BackendLLM is the oai-agent-apiserver agent.
	BackendLLM = "llm"
	// BackendCLI is a local agent-CLI process.
	BackendCLI = "cli"
)

// ErrBusy reports that a backend is still running a turn for this conversation
// and cannot take another yet. It is not a failure: a run may still be in flight
// on the backend after the caller's own bookkeeping has moved on — another
// replica's turn, or a run that outlived its caller. Callers should tell the
// user to try again rather than reporting an error.
var ErrBusy = errors.New("the agent is already running a turn for this conversation")

// ErrUnknownBackend reports a backend name no Agent is registered under.
var ErrUnknownBackend = errors.New("unknown agent backend")

// ErrNotPending reports that an answer named a pause the run is no longer
// waiting on: it was already answered, it expired, or the run moved on to a
// later pause first.
//
// It is separated from the other failures because nothing went wrong — nothing
// was executed and nothing was refused — so a caller should say the request is
// stale rather than that something broke. A card outlives the pause it was
// rendered for, which makes this the ordinary outcome of a click on an old one.
var ErrNotPending = errors.New("the answer does not match a waiting pause")

// ErrUnauthorized reports that a backend refused this server's credentials: the
// request never reached the run. It usually means an AK/SK that is missing,
// wrong, or expired, or a signature that did not survive the trip, which makes
// it an operator's problem rather than the asking user's — the card should say
// so without inviting the user to retry what cannot work.
//
// It is kept apart from ErrPermissionDenied because the two read differently to
// everyone: this one is the deployment's fault and belongs in an error log, the
// other is the user's and belongs in a sentence they can act on.
var ErrUnauthorized = errors.New("the agent backend rejected this server's credentials")

// ErrPermissionDenied reports that a backend accepted the credentials and then
// refused the run because the subject it named may not use what it asked for.
// Nothing ran and nothing executed: the user needs to be told, not shown a
// failure.
var ErrPermissionDenied = errors.New("the agent backend refused this turn for this user")

// ErrQuotaExceeded reports that a backend refused this turn because an allowance
// for this account is spent, or because a run-lifecycle hook refused it for a
// reason of its own. It is separated from ErrPermissionDenied because the two
// read differently to the person who was refused: this one is "not now" —
// waiting or raising the allowance fixes it — while that one is "not you".
//
// Unlike the permission refusals, the backend's own sentence IS the card. A
// refusal from an allowance is written in the reader's language by whichever
// side knows which allowance ran out, and that text is the whole of what the
// person can act on. See classifyPermission.
var ErrQuotaExceeded = errors.New("the agent backend refused this turn for an allowance")

// ErrTooManySessions reports that a backend has as many live conversations as it
// will hold. It is a capacity limit, not a failure: the request would succeed
// later, so a caller that can tell the user to retry should.
//
// Only a backend that holds a real resource per conversation can produce it —
// the CLI backend holds a process — which is why it is on the port rather than
// inside that backend: the caller decides what to tell the user, and it cannot
// decide that from an opaque error.
var ErrTooManySessions = errors.New("too many live agent conversations")

// Decisions a caller may answer a permission interrupt with, passed to
// Session.AnswerPermission.
//
// The vocabulary lives here rather than in either backend because it is what a
// user chose, not what a protocol calls it: every backend has to distinguish
// these three intents, and a fourth way of saying "yes, and keep saying yes to
// this tool" would be a new intent for all of them.
const (
	// DecisionAllow runs the pending tool this once.
	DecisionAllow = "allow"
	// DecisionDeny refuses it. A run that is denied does not proceed silently as
	// if it had been allowed.
	DecisionDeny = "deny"
	// DecisionAllowAll runs it and stops asking about that tool.
	DecisionAllowAll = "allow_all"
)

// AnswerRequest is one answer to a run paused on an interrupt.
type AnswerRequest struct {
	// RunID names the run being answered — the caller's own name for it, the one
	// it passed in AskRequest and put in the card's buttons. It is checked
	// against the run the backend has paused rather than trusted, so an answer
	// meant for a run this conversation no longer holds is refused instead of
	// applied to whatever replaced it. Empty answers whichever run is paused.
	RunID string
	// RequestID names the pause being answered, exactly as the backend reported
	// it on the interrupt. One run can pause more than once, and this is what
	// makes an answer apply to the pause the human was looking at rather than to
	// whatever the run waits on by the time the answer arrives. Empty answers
	// whichever pause is waiting, which is the best a backend that keeps no such
	// identity — the CLI — can offer.
	RequestID string
	// Decision is what the human decided: DecisionAllow, DecisionDeny or
	// DecisionAllowAll. A question is answered with DecisionAllow, because
	// answering is not refusing; the answer itself travels in Answers.
	Decision string
	// Answers carries answers to a question interrupt, keyed by the address of
	// the request they answer (InterruptRequest.Address). One question is put to
	// the human at a time, so this normally holds one entry.
	//
	// A pause with no address — a backend that keeps no such identity — keeps its
	// answer under the empty key. The key is then only a container: such a
	// backend reads the answer as text (AnswerText) rather than looking anything
	// up, so the convention costs it nothing.
	Answers map[string]string
	// Reason is the human's explanation for a denial, for a backend that passes
	// it on to the model. A backend that requires one supplies its own when this
	// is empty.
	Reason string
}

// AnswerText returns the text answer, for a backend whose pause has no address
// to key one by.
//
// A backend that addresses its interrupts uses Answers directly; the CLI's
// request ids never leave its own package, so the answer reaches it as text
// rather than as an entry in a map. One question is asked at a time, so the map
// holds at most one entry where this is used.
func (r AnswerRequest) AnswerText() string {
	for _, v := range r.Answers {
		return v
	}
	return ""
}

// AskRequest is one turn's input. The conversation it belongs to is carried by
// the Session, not here: a request that named its own session key could be sent
// down the wrong one.
type AskRequest struct {
	// Question is the cleaned user prompt.
	Question string
	// Images are the images attached to this turn, as references.
	//
	// A reference is an identity, not the bytes: the backend resolves it at the
	// model edge. That is what keeps a screenshot out of this service's memory
	// for more than the upload that received it, and out of the conversation
	// history the backend stores.
	Images []media.Ref
	// Reasoning asks the backend to surface its reasoning stream.
	Reasoning bool
	// Agents optionally selects a specific agent (persona) for this turn.
	Agents []string
	// InvokedSkills names the skills this turn is asked to run, because the
	// user chose a skill command rather than typing a sentence. The backend
	// refuses a name the turn's agents do not declare, so an empty list and a
	// list of names the agents have are the only two acceptable shapes.
	InvokedSkills []string
	// SenderID is the stable identity of the human asker — for Lark, the
	// tenant-scoped user_id, never an open_id (which is per-application and so
	// cannot be compared across services) and never a display name. The backend
	// forwards it to the MCP tools it calls, where it decides which records the
	// caller owns, so it must be the same value ResolveUsername yields. Empty
	// means the asker could not be identified.
	SenderID string
	// RunID identifies this run for stop signalling. The caller generates it, so
	// that it can stop a run it has not finished starting.
	RunID string
	// Resume continues the conversation on a backend that keeps its sessions by
	// id, and is whatever Session.ID() last reported for it. A backend that owns
	// its conversations ignores it: the llm backend addresses the conversation by
	// session key and holds the history itself, so there is no id to resume with
	// and an empty value here is the normal case rather than a fresh start.
	Resume string
	// WorkDir asks the turn to run in a specific directory, for a backend whose
	// turns do. Empty means the backend's own choice, which is the normal case.
	//
	// A backend that accepts it re-checks it against its own confinement rather
	// than trusting the caller: what arrives here may have been read from a
	// snapshot written before the configuration changed, and the check that
	// matters is the one made where the process is started.
	WorkDir string
}

// DirScope is implemented by a backend whose turns run in a directory, and which
// can say which directories those may be.
//
// It is a separate interface rather than part of Agent because it is not a
// backend's job to decide where it runs: the configuration decides, the backend
// reports it, and a caller that wants to offer a choice asks. A backend that
// holds no directory simply does not implement this, and a caller finds out by
// asking rather than by knowing which backend it is talking to.
type DirScope interface {
	// AllowedRoots returns the absolute trees a conversation may be pointed at,
	// the first of which is the root new conversations default to.
	AllowedRoots() []string
}

// Agent is a backend that can serve conversations.
type Agent interface {
	// Name is the backend name this Agent registers under.
	Name() string
	// Session returns the conversation bound to key, creating the binding if
	// there is not one yet. The caller owns the returned Session and must Close
	// it; an implementation may hand back a shared one.
	Session(ctx context.Context, key string) (Session, error)
	// CloseSession releases the conversation bound to key, if this backend holds
	// one. It is how a backend that keeps a real resource per conversation — a
	// process — is told the conversation is over; a backend that holds nothing
	// has nothing to do.
	//
	// Closing an unknown key is not an error: conversations are reaped and shut
	// down on paths that do not track which backends hold what.
	CloseSession(ctx context.Context, key string) error
	// Close releases every session this backend holds. The process is ending.
	Close() error
}

// Session is one conversation bound to a backend: the unit a run is started on
// and a question is answered through.
//
// Calls on one Session are serialised by the caller, not by the implementation —
// the conversations in this server are already serialised by pkg/session, and a
// second lock here would only be a second thing to deadlock on.
type Session interface {
	// ID is the identifier the backend resumes this conversation with, or "" when
	// the backend has none because it owns the conversation itself (the LLM
	// backend: agent already holds the history under the session key).
	ID() string
	// AskStream runs one turn, calling onEvent for each event in order. It
	// returns when the run terminates, or the first error from onEvent.
	//
	// A run that pauses for a decision reports it as EventInterrupted and
	// continues when someone answers it — see AnswerPermission. Whether the pause
	// also ends this stream is the backend's business: the LLM backend's does, and
	// says so with EventSuspended, while the CLI backend's process stays alive
	// holding the request, so the stream keeps running and the rest of the turn
	// arrives on it. A caller must treat EventInterrupted as the pause and
	// EventSuspended as no more than "this stream is over".
	AskStream(ctx context.Context, req AskRequest, onEvent func(Event) error) error
	// AnswerPermission answers a run paused on an interrupt.
	//
	// It returns once the answer has been delivered, not when the run finishes.
	// The continuation reaches the same event sink AskStream was given, which the
	// pause left attached: that is what lets one card render a whole turn across
	// a decision, and why this takes no callback of its own — a second callback
	// here would be a second renderer for one run. A failure that happens after
	// delivery reaches that sink as an EventFailed, since there is no caller left
	// to return it to.
	AnswerPermission(ctx context.Context, req AnswerRequest) error
	// Stop cancels an in-flight run.
	Stop(ctx context.Context, runID string) error
	// Close releases this conversation's resources. It is safe to call twice.
	Close() error
}
