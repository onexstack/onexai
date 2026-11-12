// Copyright 2025 Lingfei Kong <colin404@foxmail.com>
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

package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metric name prefixes.
//
// The legacy prefix is what this service has emitted since it was derived from
// opsassist's code, and it is the wrong name for a service called
// oai-chat-apiserver: the metrics carry another product's namespace. Renaming
// them is a breaking change for every dashboard and alert that queries them,
// so the caller decides — see Initialize.
const (
	prefixAligned = "onexai_chat"
	prefixLegacy  = "opsassist"
)

type Metrics struct {
	Meter                         metric.Meter
	LarkCommandExecuteCounter     metric.Int64Counter
	LarkCommandDuration           metric.Float64Histogram
	LarkCommandErrorCounter       metric.Int64Counter
	OncallTicketProcessedCounter  metric.Int64Counter
	OncallSummaryCounter          metric.Int64Counter
	AIRequestCounter              metric.Int64Counter
	AIRequestDuration             metric.Float64Histogram
	AIRequestErrorCounter         metric.Int64Counter
	AIToolExecutionCounter        metric.Int64Counter
	AIToolDuration                metric.Float64Histogram
	AIToolErrorCounter            metric.Int64Counter
	AIToolTokensCounter           metric.Int64Counter
	AISkillExecutionCounter       metric.Int64Counter
	AISkillDuration               metric.Float64Histogram
	AISkillErrorCounter           metric.Int64Counter
	AISkillTokensCounter          metric.Int64Counter
	AIPromptTokensCounter         metric.Int64Counter
	AICompletionTokensCounter     metric.Int64Counter
	AIReasoningTokensCounter      metric.Int64Counter
	AIHistoryCompressionCounter   metric.Int64Counter
	PanicRecoveredCounter         metric.Int64Counter
	AuthzCheckCounter             metric.Int64Counter     // <prefix>_authz_checks_total
	AuthzCheckDuration            metric.Float64Histogram // <prefix>_authz_check_duration_seconds
	AuthzDenyCounter              metric.Int64Counter     // <prefix>_authz_deny_total
	FunnelStepCounter             metric.Int64Counter     // <prefix>_inbound_funnel_steps_total
	OutboundFailureCounter        metric.Int64Counter     // <prefix>_outbound_failures_total
	DuplicateTriggerCounter       metric.Int64Counter     // <prefix>_duplicate_triggers_total
	UnresolvedIdentityCounter     metric.Int64Counter     // <prefix>_unresolved_identities_total
	AgentCommandEmptyGrantCounter metric.Int64Counter     // <prefix>_agent_command_empty_grants_total
	AgentCommandNoGrantCounter    metric.Int64Counter     // <prefix>_agent_command_no_grant_total
	AgentCommandDegradedCounter   metric.Int64Counter     // <prefix>_agent_command_resolve_degraded_total
	QuotedReplyCounter            metric.Int64Counter     // <prefix>_quoted_replies_total
}

var M *Metrics

// prefixFor selects the namespace Initialize emits under. The default (false)
// is the legacy one, so a deployment that has not opted in keeps the exact
// metric names it had before this switch existed.
func prefixFor(aligned bool) string {
	if aligned {
		return prefixAligned
	}
	return prefixLegacy
}

// instrumentBuilder creates instruments in sequence and remembers the first
// error. One Initialize body declares 33 instruments; threading an error return
// through each of them is the boilerplate this removes, without changing the
// contract that a bad metric name fails initialization.
type instrumentBuilder struct{ err error }

func (b *instrumentBuilder) counter(meter metric.Meter, name, description string) metric.Int64Counter {
	counter, err := meter.Int64Counter(name, metric.WithDescription(description))
	if err != nil && b.err == nil {
		b.err = err
	}
	return counter
}

func (b *instrumentBuilder) histogram(
	meter metric.Meter, name, description, unit string,
) metric.Float64Histogram {
	histogram, err := meter.Float64Histogram(
		name, metric.WithDescription(description), metric.WithUnit(unit))
	if err != nil && b.err == nil {
		b.err = err
	}
	return histogram
}

// Initialize initializes OpenTelemetry metrics and custom business metrics.
//
// aligned selects the metric namespace: true emits onexai_chat_*, false keeps
// the opsassist_* names this service has always used. The names are part of a
// dashboard's contract, so the switch exists to make the rename a decision
// rather than a side effect of an unrelated change.
func Initialize(_ context.Context, scope string, aligned bool) error {
	meter := otel.Meter(scope + ".metrics")

	prefix := prefixFor(aligned)
	name := func(metric string) string { return prefix + "_" + metric }

	build := &instrumentBuilder{}

	// Define Lark command metrics
	larkCommandCounter := build.counter(meter, name("lark_commands_executed_total"),
		"Total number of Lark command executions")
	larkCommandDuration := build.histogram(meter, name("lark_command_execution_duration_seconds"),
		"Duration of Lark command executions in seconds", "s")
	larkCommandErrorCounter := build.counter(meter, name("lark_command_errors_total"),
		"Total number of Lark command execution errors")
	oncallTicketProcessedCounter := build.counter(meter, name("oncall_tickets_processed_total"),
		"Total number of tickets processed by oncall groups")
	// The closing summary is generated beside the reply and reports its failures only
	// to a log line, so this counter is the difference between "no summary was
	// needed" and "the model has been down for an hour" — see RecordOncallSummary.
	oncallSummaryCounter := build.counter(meter, name("oncall_summaries_total"),
		"Closing summaries by outcome: written, brief, or failed")
	aiRequestCounter := build.counter(meter, name("ai_requests_total"),
		"Total number of AI (RunAskAI) invocations")
	aiRequestDuration := build.histogram(meter, name("ai_request_duration_seconds"),
		"Duration of AI (RunAskAI) requests from start to first token or completion", "s")
	aiRequestErrorCounter := build.counter(meter, name("ai_request_errors_total"),
		"Total number of AI (RunAskAI) invocations that resulted in an error")
	aiToolCounter := build.counter(meter, name("ai_tool_executions_total"),
		"Total number of AI tool executions")
	aiToolDuration := build.histogram(meter, name("ai_tool_execution_duration_seconds"),
		"Duration of AI tool executions in seconds", "s")
	aiToolErrorCounter := build.counter(meter, name("ai_tool_errors_total"),
		"Total number of AI tool execution errors")
	aiToolTokensCounter := build.counter(meter, name("ai_tool_tokens_estimated_total"),
		"Estimated total number of tokens used by AI tool executions")
	aiPromptTokensCounter := build.counter(meter, name("ai_prompt_tokens_estimated_total"),
		"Estimated total number of prompt tokens used")
	aiCompletionTokensCounter := build.counter(meter, name("ai_completion_tokens_estimated_total"),
		"Estimated total number of completion tokens used")
	aiReasoningTokensCounter := build.counter(meter, name("ai_reasoning_tokens_estimated_total"),
		"Estimated total number of reasoning tokens used")
	aiHistoryCompressionCounter := build.counter(meter, name("ai_history_compression_total"),
		"Total number of history compressions triggered")
	aiSkillCounter := build.counter(meter, name("ai_skill_executions_total"),
		"Total number of AI skill executions")
	aiSkillDuration := build.histogram(meter, name("ai_skill_execution_duration_seconds"),
		"Duration of AI skill executions in seconds", "s")
	aiSkillErrorCounter := build.counter(meter, name("ai_skill_errors_total"),
		"Total number of AI skill execution errors")
	aiSkillTokensCounter := build.counter(meter, name("ai_skill_tokens_estimated_total"),
		"Estimated total number of tokens used by AI skill executions")
	panicRecoveredCounter := build.counter(meter, name("panic_recovered_total"),
		"Total number of panics recovered in background goroutines")
	authzCheckCounter := build.counter(meter, name("authz_checks_total"),
		"Total number of authorization checks")
	authzCheckDuration := build.histogram(meter, name("authz_check_duration_seconds"),
		"Duration of authorization checks in seconds", "s")
	authzDenyCounter := build.counter(meter, name("authz_deny_total"),
		"Total number of authorization denials")
	funnelStepCounter := build.counter(meter, name("inbound_funnel_steps_total"),
		"Total number of inbound messages by the funnel step that decided their fate and the outcome")
	outboundFailureCounter := build.counter(meter, name("outbound_failures_total"),
		"Total number of outbound deliveries that failed, by the surface that was sending")
	duplicateTriggerCounter := build.counter(meter, name("duplicate_triggers_total"),
		"Total number of inbound messages dropped because the same message had already produced a turn")
	unresolvedIdentityCounter := build.counter(meter, name("unresolved_identities_total"),
		"Total number of inbound events that arrived without a sender identity (Lark user_id)")
	// A reply whose quoted message could not be read is answered without it,
	// which from the outside is indistinguishable from a bot that ignored what
	// was quoted. Counting every reply, by how its quote resolved, is what makes
	// that difference visible.
	quotedReplyCounter := build.counter(meter, name("quoted_replies_total"),
		"Total number of replies that quoted a message, by how the quoted content resolved")
	// A rule that covers a conversation and grants it nothing is a typo or a
	// stale name, never a deliberate state: an empty grant is written as an
	// absent rule.
	agentCommandEmptyGrantCounter := build.counter(meter, name("agent_command_empty_grants_total"),
		"Total number of resolves where a rule covered the conversation and granted nothing")
	// Command grants are deny-by-default, so a conversation that is handed no
	// command is an ordinary state — every conversation no rule covers is in
	// it. It is counted because it is also silent: a menu that failed to arrive
	// looks the same as a rule that was never written, and the reason attribute
	// is what tells a table nobody configured from one that was emptied or
	// mistyped.
	agentCommandNoGrantCounter := build.counter(meter, name("agent_command_no_grant_total"),
		"Total number of resolves that granted the conversation no command at all")
	// The command set falls back to "everything visible" when an input cannot
	// be read. That is the safe direction and it is also invisible, which is
	// what this counter is for.
	agentCommandDegradedCounter := build.counter(meter, name("agent_command_resolve_degraded_total"),
		"Total number of command resolves that could not read one of their inputs")
	if build.err != nil {
		return build.err
	}

	// Assign global instance
	M = &Metrics{
		Meter:                         meter,
		LarkCommandExecuteCounter:     larkCommandCounter,
		LarkCommandDuration:           larkCommandDuration,
		LarkCommandErrorCounter:       larkCommandErrorCounter,
		OncallTicketProcessedCounter:  oncallTicketProcessedCounter,
		OncallSummaryCounter:          oncallSummaryCounter,
		AIRequestCounter:              aiRequestCounter,
		AIRequestDuration:             aiRequestDuration,
		AIRequestErrorCounter:         aiRequestErrorCounter,
		AIToolExecutionCounter:        aiToolCounter,
		AIToolDuration:                aiToolDuration,
		AIToolErrorCounter:            aiToolErrorCounter,
		AIToolTokensCounter:           aiToolTokensCounter,
		AISkillExecutionCounter:       aiSkillCounter,
		AISkillDuration:               aiSkillDuration,
		AISkillErrorCounter:           aiSkillErrorCounter,
		AISkillTokensCounter:          aiSkillTokensCounter,
		AIPromptTokensCounter:         aiPromptTokensCounter,
		AICompletionTokensCounter:     aiCompletionTokensCounter,
		AIReasoningTokensCounter:      aiReasoningTokensCounter,
		AIHistoryCompressionCounter:   aiHistoryCompressionCounter,
		PanicRecoveredCounter:         panicRecoveredCounter,
		AuthzCheckCounter:             authzCheckCounter,
		AuthzCheckDuration:            authzCheckDuration,
		AuthzDenyCounter:              authzDenyCounter,
		FunnelStepCounter:             funnelStepCounter,
		OutboundFailureCounter:        outboundFailureCounter,
		DuplicateTriggerCounter:       duplicateTriggerCounter,
		UnresolvedIdentityCounter:     unresolvedIdentityCounter,
		AgentCommandEmptyGrantCounter: agentCommandEmptyGrantCounter,
		AgentCommandNoGrantCounter:    agentCommandNoGrantCounter,
		AgentCommandDegradedCounter:   agentCommandDegradedCounter,
		QuotedReplyCounter:            quotedReplyCounter,
	}

	return nil
}

// RecordAgentCommandEmptyGrant counts a resolve where a rule covered the
// conversation and granted it nothing.
func (m *Metrics) RecordAgentCommandEmptyGrant(ctx context.Context) {
	if m == nil {
		return
	}
	m.AgentCommandEmptyGrantCounter.Add(ctx, 1)
}

// Reasons a resolve granted a conversation nothing. They are separate because
// they call for different responses: NoGrantNoRules says the rule table is
// empty (nobody has configured the deployment, or somebody emptied it), while
// NoGrantUncovered says the table has rules and this agent is outside all of
// them (add a rule, or fix the one that was meant to cover it).
const (
	NoGrantNoRules   = "no_rules"
	NoGrantUncovered = "uncovered"
)

// RecordAgentCommandNoGrant counts a resolve that handed the conversation no
// command at all, and why.
func (m *Metrics) RecordAgentCommandNoGrant(ctx context.Context, reason string) {
	if m == nil {
		return
	}
	m.AgentCommandNoGrantCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// RecordAgentCommandDegraded counts a resolve that could not read one of its
// inputs, and therefore answered with everything visible.
//
// source names the input that failed: "rules", "agents" or "skills".
func (m *Metrics) RecordAgentCommandDegraded(ctx context.Context, source string) {
	if m == nil {
		return
	}
	m.AgentCommandDegradedCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("source", source)))
}

// RecordLarkCommand records a Lark command execution.
func (m *Metrics) RecordLarkCommand(ctx context.Context, command string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("command", command)}

	m.LarkCommandExecuteCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordLarkCommandDuration records how long one command took, from the moment
// its message was picked up to the moment the reply was sent.
//
// The histogram was defined but never written: only a counter of executions
// existed, so "the bot is slow" had no answer beyond a log line per message.
// The command name is the label because the interesting question is which
// command, not how fast the average is.
func (m *Metrics) RecordLarkCommandDuration(ctx context.Context, command string, durationSeconds float64) {
	if m == nil {
		return
	}
	m.LarkCommandDuration.Record(ctx, durationSeconds, metric.WithAttributes(attribute.String("command", command)))
}

// RecordLarkCommandError records a Lark command execution error.
func (m *Metrics) RecordLarkCommandError(ctx context.Context, command string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("command", command)}

	m.LarkCommandErrorCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordOncallTicketProcessed records a ticket processed by an oncall group.
// oncallGroup: parent group code used for aggregation.
// subGroup: sub-group code if the ticket was assigned to a sub-group, otherwise empty string.
func (m *Metrics) RecordOncallTicketProcessed(ctx context.Context, oncallGroup, subGroup string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("oncall_group", oncallGroup),
		attribute.String("sub_group", subGroup),
	}

	m.OncallTicketProcessedCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// Closing-summary outcomes, and they are not a status: "brief" is a successful
// generation whose answer was 「无需复盘」 (no retrospective needed), while "failed" produced nothing at all. A
// reader who could not tell them apart would read a working feature as a broken one
// whenever the tickets happened to be simple.
const (
	SummaryOutcomeWritten = "written"
	SummaryOutcomeBrief   = "brief"
	SummaryOutcomeFailed  = "failed"
)

// RecordOncallSummary records how one closing summary ended.
//
// It exists because that work happens after the command has already answered: a
// failure reaches the group as one message and the log as one line, and a hundred of
// them look exactly like none. This is what an alert can be written against, and the
// outcome split is what keeps 「记录简略」 (brief record) from being counted as a failure.
func (m *Metrics) RecordOncallSummary(ctx context.Context, outcome string) {
	if m == nil {
		return
	}
	m.OncallSummaryCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// RecordAIRequest increments the AI request counter.
// chatType should be "p2p" or "group".
func (m *Metrics) RecordAIRequest(ctx context.Context, chatType, userName string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("chat_type", chatType),
		attribute.String("user_name", userName),
	}
	m.AIRequestCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordAIRequestDuration records the total duration of an AI request in seconds.
// chatType should be "p2p" or "group".
func (m *Metrics) RecordAIRequestDuration(ctx context.Context, durationSeconds float64) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{}
	m.AIRequestDuration.Record(ctx, durationSeconds, metric.WithAttributes(attrs...))
}

// RecordAIRequestError increments the AI request error counter.
// chatType should be "p2p" or "group". errType is a short error category label
// (e.g., "deadline_exceeded", "service_unavailable", "runner_error").
func (m *Metrics) RecordAIRequestError(ctx context.Context, errType string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("error_type", errType)}
	m.AIRequestErrorCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordAIToolExecution increments the tool execution counter.
func (m *Metrics) RecordAIToolExecution(ctx context.Context, toolName, chatType, userName string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("tool_name", toolName),
		attribute.String("chat_type", chatType),
		attribute.String("user_name", userName),
	}
	m.AIToolExecutionCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordAIToolDuration records the duration of an AI tool execution.
func (m *Metrics) RecordAIToolDuration(ctx context.Context, toolName, chatType, userName string, durationSeconds float64) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("tool_name", toolName),
		attribute.String("chat_type", chatType),
		attribute.String("user_name", userName),
	}
	m.AIToolDuration.Record(ctx, durationSeconds, metric.WithAttributes(attrs...))
}

// RecordAIToolError increments the AI tool error counter.
func (m *Metrics) RecordAIToolError(ctx context.Context, toolName, chatType, userName string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("tool_name", toolName),
		attribute.String("chat_type", chatType),
		attribute.String("user_name", userName),
	}
	m.AIToolErrorCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordAIToolTokens increments the estimated tool token counter.
func (m *Metrics) RecordAIToolTokens(ctx context.Context, toolName, chatType, userName string, tokens int64) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("tool_name", toolName),
		attribute.String("chat_type", chatType),
		attribute.String("user_name", userName),
	}
	m.AIToolTokensCounter.Add(ctx, tokens, metric.WithAttributes(attrs...))
}

// RecordAITokens increments the estimated prompt, completion, and reasoning token counters.
func (m *Metrics) RecordAITokens(ctx context.Context, chatType, userName string, prompt, completion, reasoning int64) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("chat_type", chatType),
		attribute.String("user_name", userName),
	}
	m.AIPromptTokensCounter.Add(ctx, prompt, metric.WithAttributes(attrs...))
	m.AICompletionTokensCounter.Add(ctx, completion, metric.WithAttributes(attrs...))
	m.AIReasoningTokensCounter.Add(ctx, reasoning, metric.WithAttributes(attrs...))
}

// RecordAISkillExecution increments the skill execution counter.
func (m *Metrics) RecordAISkillExecution(ctx context.Context, skillName, chatType, userName string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("skill_name", skillName),
		attribute.String("chat_type", chatType),
		attribute.String("user_name", userName),
	}
	m.AISkillExecutionCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordAISkillDuration records the duration of an AI skill execution.
func (m *Metrics) RecordAISkillDuration(ctx context.Context, skillName, chatType, userName string, durationSeconds float64) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("skill_name", skillName),
		attribute.String("chat_type", chatType),
		attribute.String("user_name", userName),
	}
	m.AISkillDuration.Record(ctx, durationSeconds, metric.WithAttributes(attrs...))
}

// RecordAISkillError increments the AI skill error counter.
func (m *Metrics) RecordAISkillError(ctx context.Context, skillName, chatType, userName string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("skill_name", skillName),
		attribute.String("chat_type", chatType),
		attribute.String("user_name", userName),
	}
	m.AISkillErrorCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordAISkillTokens increments the estimated skill token counter.
func (m *Metrics) RecordAISkillTokens(ctx context.Context, skillName, chatType, userName string, tokens int64) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("skill_name", skillName),
		attribute.String("chat_type", chatType),
		attribute.String("user_name", userName),
	}
	m.AISkillTokensCounter.Add(ctx, tokens, metric.WithAttributes(attrs...))
}

// RecordAIHistoryCompression increments the LLM context compression counter.
func (m *Metrics) RecordAIHistoryCompression(ctx context.Context, chatType, userName string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("chat_type", chatType),
		attribute.String("user_name", userName),
	}
	m.AIHistoryCompressionCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordPanicRecovered increments the recovered-panic counter. source identifies
// the recovery site (e.g. "lark.message", "lark.card_action") so an alert can
// tell a crashing command apart from a crashing background job.
//
// Recovering a panic turns a process crash into a log line, so this counter is
// the only thing that keeps the failure visible — alert on it.
func (m *Metrics) RecordPanicRecovered(ctx context.Context, source string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("source", source)}
	m.PanicRecoveredCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordFunnelStep counts what the inbound ladder did with one message: which
// step decided its fate, and whether that was to handle it, route it, or drop it.
//
// Every terminal branch reports here, including the drops that send no reply.
// From the outside those are indistinguishable — an allowlist, a staleness bound
// and a missing @-mention all look exactly like a bot that is ignoring you — and
// "the bot did not answer me" is otherwise unanswerable without reading logs.
func (m *Metrics) RecordFunnelStep(ctx context.Context, step, outcome string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("step", step),
		attribute.String("outcome", outcome),
	}
	m.FunnelStepCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordQuotedReply counts a reply by how the message it quoted resolved:
// resolved (read, and it fits), truncated (read but cut to the prompt budget),
// empty (the platform returned nothing usable — recalled, deleted, body-less),
// or failed (the read itself failed).
//
// The four are mutually exclusive and every reply produces exactly one, so the
// sum is the number of replies the bot received. That denominator is the point:
// "we answered without the quote" is only a problem at some rate, and without
// one a silent degradation and a healthy bot look the same.
func (m *Metrics) RecordQuotedReply(ctx context.Context, outcome string) {
	if m == nil {
		return
	}
	m.QuotedReplyCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// RecordOutboundFailure counts a delivery that did not reach the user, by the
// surface that was sending it.
//
// This exists because the failure used to exist only as a discarded error
// return. A reply that never arrived is indistinguishable, from every seat
// except the process itself, from one the bot never tried to send — and the
// first symptom was a user saying the bot ignored them. Counting it where the
// send happens is the cheapest way to make it answerable.
func (m *Metrics) RecordOutboundFailure(ctx context.Context, surface string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("surface", surface)}
	m.OutboundFailureCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordDuplicateTrigger counts an inbound message that was dropped because the
// same message had already produced a turn.
//
// A duplicate the platform re-delivers is indistinguishable, from the user's
// seat, from a message the bot ignored: no second answer arrives and no error is
// reported. It used to be worse than that — the duplicate was admitted, killed
// the running turn and left a second card — so this counts the drops that make
// the invariant hold, and it is the number to watch when asking whether the
// delivery layer ever retries at all.
func (m *Metrics) RecordDuplicateTrigger(ctx context.Context) {
	if m == nil {
		return
	}
	m.DuplicateTriggerCounter.Add(ctx, 1)
}

// RecordUnresolvedIdentity counts an inbound event whose sender carried no
// identity. The identity is the sender's Lark user_id, which the platform only
// reports when the app may read user IDs; when this counter starts moving,
// either that permission was revoked or the sender is external. It is the
// number to watch after a permission change — an unresolved identity used to be
// papered over with the sender's open_id, which is how the same person ended up
// with two identities.
func (m *Metrics) RecordUnresolvedIdentity(ctx context.Context, source string) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("source", source)}
	m.UnresolvedIdentityCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}
