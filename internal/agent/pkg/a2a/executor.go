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

// Package a2a exposes the agent over the Agent-to-Agent protocol.
//
// It is a thin adapter, not a second execution path: an A2A request becomes the
// same Ask the chat and OpenAI-compatible surfaces make, and the events that
// come back are translated into A2A events. Anything the protocol needs that
// the agent does not have — a per-request history, an approval card — is
// handled by mapping it onto what exists rather than by growing a parallel
// implementation inside the adapter.
package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/onexstack/onexai/internal/agent/pkg/a2aevent"
	"github.com/onexstack/onexai/internal/agent/pkg/agentedge"
	"github.com/onexstack/onexai/internal/pkg/authz"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

// AskRequestAuthzService names the caller the A2A surface falls back to when a
// request carries no authenticated identity — a deployment with the AK/SK check
// bypassed, in practice. An authenticated request is recorded as the caller
// that signed it, not as this.
const AskRequestAuthzService = "a2a-executor"

// The three ways a request cannot be turned into a run, plus a refused turn.
var (
	errNoMessage       = errors.New("a2a: the request carries no message")
	errNoTaskIdentity  = errors.New("a2a: the request has no task or context identity")
	errUnsupportedPart = errors.New("a2a: unsupported part; send text or an image URL")
	errInputRefused    = errors.New("a2a: the input was refused")
)

// Runner is the narrow surface this adapter needs: run one turn and stream its
// events. agentapi.IAgentBiz satisfies it.
type Runner interface {
	Ask(ctx context.Context, req *agentapi.AskRequest) (<-chan agentapi.Event, error)
}

// Approver answers a paused run. The A2A continuation carries a HITL response,
// so the adapter resumes the stored run instead of starting a new turn.
type Approver interface {
	Approve(ctx context.Context, req *agentapi.ApproveRequest) (<-chan agentapi.Event, error)
}

// Stopper ends a run by id. It is separate from Runner because a deployment can
// have a runner that cannot stop anything (an engine without a stop registry),
// and cancel then degrades to reporting the state rather than failing.
type Stopper interface {
	Stop(ctx context.Context, runID string) error
}

// Turn is one message about to reach the model, as the inbound audit needs to
// see it.
type Turn struct {
	Source     string
	SessionKey string
	SenderID   string
	SenderName string
	Text       string
}

// Guard screens a turn. It returns the reason to report when the turn must not
// run, and whether it must be refused.
//
// It is an interface rather than a *inputaudit.Screener because a refusal also
// has to be recorded, and recording is the edge's business: the adapter asks,
// the handler package answers with the same screening, metrics and audit row
// the chat surface uses.
type Guard interface {
	Screen(ctx context.Context, turn Turn) (reason string, blocked bool)
}

// AgentLookup answers whether an agent a request names exists.
//
// It exists because the run would otherwise accept a mistyped name silently: the
// selection resolves unknown names to nothing, so the caller gets a normal answer
// from an agent with no instruction and no tools, which looks like a working
// deployment and is not.
type AgentLookup interface {
	Exists(ctx context.Context, name string) (bool, error)
}

// unknownAgentError names an agent the caller asked for and the deployment does
// not have.
type unknownAgentError struct {
	name string
}

func (e *unknownAgentError) Error() string {
	return "a2a: agent " + e.name + " not found"
}

// Executor maps A2A task execution onto the agent.
type Executor struct {
	runner Runner
	// approver resumes a paused run. Nil means this surface cannot answer a
	// pause and the task fails instead.
	approver Approver
	// stopper ends the run behind a task on cancel. Nil when the deployment has
	// no stop registry.
	stopper Stopper
	// guard screens a turn before it costs anything. Nil does not screen.
	guard Guard
	// lookup refuses a request that names an agent this deployment does not
	// have. Nil skips the check, which is what a deployment with no resource
	// store can do.
	lookup AgentLookup
	// defaultAgent names the agent set a request selects when it does not
	// select one itself. Empty means the server's own default.
	defaultAgent string
	// source names this surface on the run record.
	source string
}

// NewExecutor returns an Executor over the given runner.
func NewExecutor(runner Runner, opts Options) *Executor {
	executor := &Executor{
		runner:       runner,
		defaultAgent: opts.DefaultAgent,
		guard:        opts.Guard,
		lookup:       opts.Lookup,
		source:       Source,
	}
	if approver, ok := runner.(Approver); ok {
		executor.approver = approver
	}
	// The runner is the agent service, which can stop a run; a test double
	// usually cannot. Asserting rather than demanding keeps the constructor's
	// signature honest about what execution needs and what cancel adds.
	if stopper, ok := runner.(Stopper); ok {
		executor.stopper = stopper
	}
	return executor
}

// Execute runs one A2A task, yielding the protocol's events.
//
// The translation is deliberately lossy in one direction: the agent's tool
// steps, reasoning and usage have no A2A representation and are dropped. What
// survives is the answer, which is what the protocol carries.
func (e *Executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
				return
			}
		}
		if !yield(a2aevent.Status(execCtx, a2a.TaskStateWorking, ""), nil) {
			return
		}

		if e.runner == nil {
			// The surface is mounted in every deployment, so a build with no
			// agent engine says so per task rather than by a missing route,
			// which is indistinguishable from a mistyped URL.
			yield(a2aevent.Status(execCtx, a2a.TaskStateFailed, "the agent engine is not configured"), nil)
			return
		}

		var events <-chan agentapi.Event
		approval, isApproval, err := e.approveRequestFor(execCtx)
		if err != nil {
			slog.WarnContext(ctx, "a2a: unusable approval response", "task", execCtx.TaskID, "error", err)
			yield(a2aevent.Status(execCtx, a2a.TaskStateFailed, refusalText(err)), nil)
			return
		}
		if isApproval {
			if e.approver == nil {
				yield(a2aevent.Status(execCtx, a2a.TaskStateFailed,
					"the run needs a human approval, which this deployment cannot resume"), nil)
				return
			}
			events, err = e.approver.Approve(ctx, approval)
			if err != nil {
				slog.WarnContext(ctx, "a2a: could not resume the run", "task", execCtx.TaskID, "error", err)
				yield(nil, err)
				return
			}
		} else {
			// requestErr, not err: a := here would shadow the err the terminal
			// check below reads, and a failed run would then be reported as an
			// empty success — the caller waiting on an answer nobody produces.
			request, requestErr := e.requestFor(ctx, execCtx)
			if requestErr != nil {
				slog.WarnContext(ctx, "a2a: unusable request", "task", execCtx.TaskID, "error", requestErr)
				yield(a2aevent.Status(execCtx, a2a.TaskStateFailed, refusalText(requestErr)), nil)
				return
			}
			if reason, blocked := e.screen(ctx, execCtx, request); blocked {
				// The refusal is the caller's to see and costs nothing, which is
				// the same bargain the chat surface makes.
				slog.WarnContext(ctx, "a2a: the input was refused", "task", execCtx.TaskID, "reason", reason)
				yield(a2aevent.Status(execCtx, a2a.TaskStateFailed, reason), nil)
				return
			}
			events, err = e.runner.Ask(ctx, request)
		}
		if err != nil {
			// Reported as a failed task rather than as a transport error. An
			// error yielded with a nil event ends the executor without a terminal
			// event, and a caller waiting on the event queue then waits forever
			// for a task that will never finish. The caller can act on "failed"
			// plus a reason; it cannot act on a hang. The cause stays in the log,
			// where it can carry upstream detail the caller should not receive.
			slog.WarnContext(ctx, "a2a: could not start the run", "task", execCtx.TaskID, "error", err)
			yield(statusEventWithUsage(execCtx, a2a.TaskStateFailed, runStartRefusedText, nil), nil)
			return
		}

		if events == nil {
			// A nil channel blocks forever, and this goroutine holds the task's
			// producer: the caller would wait for an answer that nobody is
			// producing, with nothing in the log to say so. Failing is the only
			// answer that can be acted on.
			slog.ErrorContext(ctx, "a2a: the run produced no event stream", "task", execCtx.TaskID,
				"approval", isApproval)
			yield(statusEventWithUsage(execCtx, a2a.TaskStateFailed, "the run produced no answer", nil), nil)
			return
		}
		ok, usage := e.stream(ctx, execCtx, events, yield)
		if !ok {
			return
		}
		yield(statusEventWithUsage(execCtx, a2a.TaskStateCompleted, "", usage), nil)
	}
}

// stream translates the run's events, reporting false when the consumer stopped.
//
// The answer is one artifact, appended to as the model streams: a fresh
// artifact per chunk would make every token a separate result, which is what a
// client would then show.
func (e *Executor) stream(
	ctx context.Context,
	execCtx *a2asrv.ExecutorContext,
	events <-chan agentapi.Event,
	yield func(a2a.Event, error) bool,
) (bool, *agentapi.Usage) {
	var artifactID a2a.ArtifactID
	var usage *agentapi.Usage

	for {
		var event agentapi.Event
		select {
		case <-ctx.Done():
			// The client is gone. Ranging over the channel alone would park this
			// goroutine until the run ends, holding the task's producer for a
			// reader that will never come back.
			return false, usage
		case next, ok := <-events:
			if !ok {
				return true, usage
			}
			event = next
		}
		switch event.Type {
		case agentapi.EventAnswer:
			if event.Delta == "" {
				continue
			}
			if artifactID == "" {
				first := a2a.NewArtifactEvent(execCtx, a2a.NewTextPart(event.Delta))
				artifactID = first.Artifact.ID
				if !yield(first, nil) {
					return false, usage
				}
				continue
			}
			if !yield(a2a.NewArtifactUpdateEvent(execCtx, artifactID, a2a.NewTextPart(event.Delta)), nil) {
				return false, usage
			}
		case agentapi.EventFailed:
			// The cause is logged rather than published: it can carry upstream
			// addresses and vendor messages, and the caller can act only on the
			// fact that the run failed.
			slog.WarnContext(ctx, "a2a: the run failed", "task", execCtx.TaskID, "error", event.Err)
			yield(statusEventWithUsage(execCtx, a2a.TaskStateFailed, "the run failed", usage), nil)
			return false, usage
		case agentapi.EventStopped:
			yield(statusEventWithUsage(execCtx, a2a.TaskStateCanceled, "", usage), nil)
			return false, usage
		case agentapi.EventInterrupted:
			if event.Interrupt == nil {
				yield(a2aevent.Status(execCtx, a2a.TaskStateFailed, "the run paused without a request"), nil)
				return false, usage
			}
			yield(interruptStatus(execCtx, event.Interrupt), nil)
			return false, usage
		case agentapi.EventSuspended:
			yield(a2aevent.Status(execCtx, a2a.TaskStateFailed, "the run suspended before a request was recorded"), nil)
			return false, usage
		case agentapi.EventDone:
			// An engine that reports no deltas carries the whole answer in the
			// terminal event, and the other surfaces fall back to it for exactly
			// that reason. Without this the caller would get an empty task.
			if event.Usage != nil {
				usage = event.Usage
			}
			// A validated structured answer is published as data, not text: the
			// schema is the contract, and a peer that reads the JSON gets the
			// document without parsing it back out of prose.
			if event.OutputSchemaSHA256 != "" && event.Final != "" {
				if !yield(structuredArtifact(ctx, execCtx, event.Final, event.OutputSchemaSHA256), nil) {
					return false, usage
				}
				continue
			}
			if artifactID == "" && event.Final != "" {
				if !yield(a2a.NewArtifactEvent(execCtx, a2a.NewTextPart(event.Final)), nil) {
					return false, usage
				}
			}
			continue
		case agentapi.EventUsage:
			if event.Usage != nil {
				usage = event.Usage
			}
			continue
		case agentapi.EventToolStep, agentapi.EventProgress, agentapi.EventReasoning, agentapi.EventImage,
			agentapi.EventSources:
			// No A2A representation; dropped rather than misrepresented as
			// answer text, which would make the caller's transcript read as if
			// the model had said its own tool names.
			//
			// Images are named here rather than left to the default because the
			// omission is a decision, not an oversight: an A2A artifact can
			// carry bytes, but the reference the event holds is an asset id or
			// a URL on this service's own read path, which the peer has no
			// credential for.
			continue
		}
	}
}

// Cancel reports the task as cancelled, and ends the run behind it.
//
// Stopping is best effort. The protocol's cancel arrives on a different request
// than the one streaming the answer, so the only handle is the run id, which
// this adapter pins to the task id — that is enough for the stop registry,
// which is cross-process when Redis is configured. A failure to stop is logged
// rather than reported: the task is cancelled either way, and the run's own
// deadline bounds what is left of it.
func (e *Executor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if e.stopper != nil && execCtx.TaskID != "" {
			if err := e.stopper.Stop(ctx, string(execCtx.TaskID)); err != nil {
				slog.WarnContext(ctx, "a2a: could not stop the run behind the task",
					"task", execCtx.TaskID, "error", err)
			}
		}
		yield(a2aevent.Status(execCtx, a2a.TaskStateCanceled, ""), nil)
	}
}

// screen asks the inbound audit about this turn, if one is configured.
func (e *Executor) screen(
	ctx context.Context, execCtx *a2asrv.ExecutorContext, request *agentapi.AskRequest,
) (string, bool) {
	if e.guard == nil {
		return "", false
	}
	return e.guard.Screen(ctx, Turn{
		Source:     request.Source,
		SessionKey: request.SessionID,
		SenderID:   request.Subject.ID,
		SenderName: request.Subject.Name,
		Text:       request.Input.Text,
	})
}

// requestFor builds the Ask that answers one A2A task.
//
// The conversation key is the A2A context, not the task: a client that opens a
// second task in the same context is continuing the same conversation, and the
// SDK guarantees a context id for every new task. The task id is the fallback
// and, separately, the run id — which is what a later cancel needs in order to
// find the run.
//
// Only the newest message is sent as the question: the agent's own session
// already holds the earlier ones, and resending them would duplicate every turn.
// approveRequestFor turns an A2A continuation carrying a HITL response into the
// approval the agent service resumes. It returns isApproval=false for an
// ordinary message.
func (e *Executor) approveRequestFor(execCtx *a2asrv.ExecutorContext) (*agentapi.ApproveRequest, bool, error) {
	if execCtx.Message == nil || execCtx.Message.Metadata == nil {
		return nil, false, nil
	}
	raw, ok := execCtx.Message.Metadata[HITLExtensionURI]
	if !ok {
		return nil, false, nil
	}
	response, ok := DecodeHITLResponse(raw)
	if !ok {
		return nil, false, nil
	}
	if response.ID == "" || execCtx.TaskID == "" {
		return nil, true, errNoTaskIdentity
	}
	decision := agentapi.DecisionAllow
	var reasons []string
	for _, approval := range response.Approvals {
		if approval.Approved {
			continue
		}
		decision = agentapi.DecisionDeny
		if approval.RejectionReason != "" {
			reasons = append(reasons, approval.RejectionReason)
		}
	}
	// A refusal outranks allow-all: a peer that listed one rejection meant it,
	// and turning that into "approve everything" is the one reading nobody
	// would forgive.
	if response.AllowAll && decision != agentapi.DecisionDeny {
		decision = agentapi.DecisionAllowAll
	}
	answers := make(map[string]string, len(response.Answers))
	for id, values := range response.Answers {
		if len(values) > 0 {
			answers[id] = values[0]
		}
	}
	return &agentapi.ApproveRequest{
		RunID:     string(execCtx.TaskID),
		RequestID: response.ID,
		Decision:  decision,
		Answers:   answers,
		Reason:    strings.Join(reasons, "; "),
	}, true, nil
}

// structuredArtifact publishes a schema-validated answer as an
// application/json DataPart naming the schema it satisfies.
//
// The text form is kept as a second part rather than dropped: a client that
// does not understand DataParts must still be able to read the answer, and the
// extension is optional by contract.
func structuredArtifact(
	ctx context.Context, execCtx *a2asrv.ExecutorContext, final, digest string,
) *a2a.TaskArtifactUpdateEvent {
	var document any
	text := a2a.NewTextPart(final)
	if err := json.Unmarshal([]byte(final), &document); err != nil {
		// The gateway validated this answer, so a parse failure here means the
		// two disagree about what was validated. Publishing the text is the
		// honest degradation; inventing an empty document would not be.
		slog.WarnContext(ctx, "a2a: the structured answer could not be re-read as JSON",
			"task", execCtx.TaskID, "error", err)
		return a2a.NewArtifactEvent(execCtx, text)
	}
	data := a2a.NewDataPart(document)
	data.MediaType = "application/json"
	data.Metadata = map[string]any{
		PartTypeMetadataKey:        "structured-output",
		OutputSchemaSHA256Metadata: digest,
	}
	return a2a.NewArtifactEvent(execCtx, data, text)
}

// interruptStatus publishes a pause as input-required with the versioned HITL
// payload. The text part is the same hint a client without extension support
// shows.
func interruptStatus(execCtx *a2asrv.ExecutorContext, interrupt *agentapi.Interrupt) *a2a.TaskStatusUpdateEvent {
	request := HITLRequestFromInterrupt(interrupt)
	event := a2aevent.Status(execCtx, a2a.TaskStateInputRequired, request.Hint)
	if event.Status.Message == nil {
		event.Status.Message = &a2a.Message{
			ID:        a2a.NewMessageID(),
			Role:      a2a.MessageRoleAgent,
			ContextID: execCtx.ContextID,
			TaskID:    execCtx.TaskID,
		}
	}
	event.Status.Message.Extensions = []string{HITLExtensionURI}
	if event.Status.Message.Metadata == nil {
		event.Status.Message.Metadata = make(map[string]any, 1)
	}
	event.Status.Message.Metadata[HITLExtensionURI] = request.Map()
	return event
}

func (e *Executor) requestFor(ctx context.Context, execCtx *a2asrv.ExecutorContext) (*agentapi.AskRequest, error) {
	if execCtx.Message == nil {
		return nil, errNoMessage
	}
	question, images, documents, err := contentOf(execCtx.Message)
	if err != nil {
		return nil, err
	}
	// An image-only turn is a real question ("what is in this picture"), so the
	// emptiness check is about having nothing at all, not about having no text.
	if strings.TrimSpace(question) == "" && len(images) == 0 {
		return nil, errNoMessage
	}

	sessionKey := execCtx.ContextID
	if sessionKey == "" {
		sessionKey = string(execCtx.TaskID)
	}
	if sessionKey == "" {
		return nil, errNoTaskIdentity
	}

	agents := e.agentsFor(execCtx)
	if err := e.validateAgents(ctx, agents); err != nil {
		return nil, err
	}

	return &agentapi.AskRequest{
		SessionID: sessionKey,
		RunID:     string(execCtx.TaskID),
		Input:     agentapi.Input{Text: question, Images: images, Documents: documents},
		Selection: agentapi.Selection{Agents: agents},
		Subject:   e.subjectFor(ctx, execCtx),
		Metering:  agentapi.MeteringRuntime,
		Options: agentapi.ExecutionOptions{
			// A pause becomes an input-required task; the caller answers it
			// with a HITL response on the same context/task.
			Approval: agentapi.Approval{Channel: agentapi.ApprovalChannelInteractive},
		},
		Source: e.source,
	}, nil
}

// validateAgents refuses a selection naming an agent that does not exist.
func (e *Executor) validateAgents(ctx context.Context, agents []string) error {
	if e.lookup == nil {
		return nil
	}
	for _, name := range agents {
		exists, err := e.lookup.Exists(ctx, name)
		if err != nil {
			return fmt.Errorf("a2a: resolve agent %q: %w", name, err)
		}
		if !exists {
			return &unknownAgentError{name: name}
		}
	}
	return nil
}

// subjectFor names the caller a run is authorized and recorded as.
//
// The authenticated caller is the one that signed the request: it travels in
// the CallContext (set by the identity interceptor from the AK/SK subject) and
// in the request context the SDK derived from it. The constant is a last
// resort for a deployment that authenticates nobody, and it is logged, because
// a ledger row attributed to the surface itself reads like a real caller.
func (e *Executor) subjectFor(ctx context.Context, execCtx *a2asrv.ExecutorContext) agentapi.Principal {
	if execCtx.User != nil && execCtx.User.Name != "" {
		return agentapi.Principal{
			ID:   execCtx.User.Name,
			Name: execCtx.User.Name,
			Kind: principalKind(userKind(execCtx.User)),
		}
	}
	if subject, ok := authz.SubjectFromContext(ctx); ok {
		if id := subject.PrimaryID(); id != "" {
			return agentapi.Principal{
				ID:   id,
				Name: subject.UserName,
				Kind: principalKind(string(subject.Kind)),
			}
		}
	}
	slog.WarnContext(ctx, "a2a: the request carries no authenticated caller; the run is recorded as the surface",
		"task", execCtx.TaskID)
	return agentapi.Principal{
		ID:   AskRequestAuthzService,
		Name: AskRequestAuthzService,
		// The fallback is this surface itself, which is a service, not a person.
		Kind: agentapi.PrincipalKindService,
	}
}

// principalKind maps an authorization subject kind onto the kind a run records.
//
// The two vocabularies are identical on purpose, but the mapping is explicit
// rather than a cast: the values cross a package boundary, and a silent rename
// on one side would otherwise turn every caller into a user.
func principalKind(kind string) string {
	switch kind {
	case string(authz.SubjectKindServiceAccount):
		return agentapi.PrincipalKindService
	case string(authz.SubjectKindGroup):
		return agentapi.PrincipalKindGroup
	default:
		return agentapi.PrincipalKindUser
	}
}

// userKind reads the kind the identity interceptor recorded on the A2A user.
func userKind(user *a2asrv.User) string {
	if user == nil || user.Attributes == nil {
		return ""
	}
	kind, _ := user.Attributes[userKindAttribute].(string)
	return kind
}

// agentsFor reads the agent selection from the request metadata, falling back
// to the configured default.
//
// The request-level metadata is read first: it is the one a transport-wide
// client sets, while the message metadata belongs to the message the client
// both wrote and remembered. Both spellings are accepted because the singular is
// what a person writes and the plural is what an agent framework emits.
func (e *Executor) agentsFor(execCtx *a2asrv.ExecutorContext) []string {
	var msgMetadata map[string]any
	if execCtx.Message != nil {
		msgMetadata = execCtx.Message.Metadata
	}
	if names := SelectedAgents(execCtx.Metadata, msgMetadata); len(names) > 0 {
		return names
	}
	if e.defaultAgent != "" {
		return []string{e.defaultAgent}
	}
	return nil
}

// contentOf reads a message into a question and the images it carries.
//
// An image arrives as a URL and becomes a media reference, which is the form the
// whole media design uses: the model edge resolves it to bytes, and the session
// store keeps the reference rather than the picture. Inline bytes and structured
// data are refused rather than skipped — a client that sends them is either
// misconfigured or expecting an ability this surface does not have, and dropping
// them silently would answer the question without the evidence it was asked
// about.
func contentOf(message *a2a.Message) (string, []media.Ref, []media.Ref, error) {
	if message == nil {
		return "", nil, nil, errNoMessage
	}

	var text strings.Builder
	var images, documents []media.Ref
	for _, part := range message.Parts {
		if part == nil {
			continue
		}
		if value := part.Text(); value != "" {
			text.WriteString(value)
			continue
		}
		if url := part.URL(); url != "" {
			ref := media.Ref{
				URL:      string(url),
				MimeType: part.MediaType,
				Name:     part.Filename,
				Source:   media.SourceURL,
			}
			switch {
			case isImageType(part.MediaType):
				images = append(images, ref)
			case agentedge.IsTextDocument(part.MediaType, part.Filename):
				// A document is read into the question rather than attached as
				// bytes: the model reads text, not a file it cannot open.
				documents = append(documents, ref)
			default:
				return "", nil, nil, errUnsupportedPart
			}
			continue
		}
		return "", nil, nil, errUnsupportedPart
	}
	return text.String(), images, documents, nil
}

// isImageType reports whether a media type names an image this deployment can
// hand to a model. The list matches the formats the media package accepts.
func isImageType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png", "image/jpeg", "image/webp":
		return true
	}
	return false
}

// refusalText is the caller-facing sentence for a request that cannot be run.
func refusalText(err error) string {
	switch {
	case errors.Is(err, errNoMessage):
		return "the request carries no message text"
	case errors.Is(err, errUnsupportedPart):
		return "unsupported part; send text, an image URL (image/png, image/jpeg, image/webp) or a text document (md/txt/json/yaml/csv)"
	case errors.Is(err, errNoTaskIdentity):
		return "the request has no task or context identity"
	}
	var unknown *unknownAgentError
	if errors.As(err, &unknown) {
		return "agent " + unknown.name + " not found"
	}
	return errInputRefused.Error()
}

// runStartRefusedText is what a caller is told when a run could not start.
//
// It is deliberately unspecific: the cause can name an upstream the caller has
// no business seeing, and the actionable cases — a busy conversation, a refused
// agent — are the operator's to see in the log before they become a caller's to
// act on. A caller can do something with "failed"; it cannot act on a hang.
const runStartRefusedText = "the run could not be started"

// statusEventWithUsage attaches the task-lifetime token usage to a terminal
// status under the versioned usage extension.
func statusEventWithUsage(
	execCtx *a2asrv.ExecutorContext, state a2a.TaskState, text string, usage *agentapi.Usage,
) *a2a.TaskStatusUpdateEvent {
	return a2aevent.StatusWithUsage(execCtx, state, text, a2aevent.UsageFromAgentAPI(usage))
}
