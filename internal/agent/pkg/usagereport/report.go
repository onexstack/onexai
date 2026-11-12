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

// Package usagereport sends a finished run's usage back to the platform that
// owns the ledger.
//
// # Why the runtime reports at all
//
// Most runs are already accounted for where they end: OneX's agent service
// consumes the run's events, sees the terminal usage event, and writes the rows.
// A Lark conversation is the exception — the chat service talks to this service
// directly, so nothing on the platform side ever sees that run, and its tokens
// used to reach Prometheus and a card footer and nothing else. The ledger then
// answered "what did the group chat spend" with a number that excluded it.
//
// # Why best-effort, and why that is not a shortcut
//
// The report is made after the answer has been delivered, on a context of its
// own with a short deadline, and a failure only logs. That ordering is the
// point: an answer is what the user asked for, and a run that answered
// correctly must not be turned into an error — or delayed — by a bookkeeping hop
// that failed. The cost is that the ledger can be short a row, which the log
// then names (with the run ID) rather than hiding.
package usagereport

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	usageclient "github.com/onexstack/onexai/internal/agent/pkg/clientset/typed/usage"
	"github.com/onexstack/onexai/internal/agent/pkg/usage"
)

// PathRecordUsage is the OneX endpoint this reports to. It is the same path
// constant OneX's own control-API contract declares (`agentctl.PathRecordUsage`);
// this service is a different module and cannot import it, so the spelling is
// repeated here and the pair is what a contract test on either side pins.
const PathRecordUsage = "/internal/v1/agent-usage"

// HeaderToken is the shared secret's header name, matching OneX's
// `agentctl.HeaderToken` (which aliases pkg/known's own constant).
const HeaderToken = "X-Onex-Internal-Token"

// Record is one finished run, as the platform needs it.
type Record struct {
	// UserID is the account the run's usage belongs to. It is the asker's
	// subject: the platform account for a speaker OneX has bound, and a chat
	// identity otherwise — the platform records the latter rather than dropping
	// it, because consumption that happened is not made cheaper by being
	// unattributable.
	UserID string
	// SessionKey is the conversation the run belongs to, so usage can be traced
	// back to a thread.
	SessionKey string
	// RunID keys the rows, and matches what the answer's card carries.
	RunID string
	// KnowledgeBases is how many knowledge bases the run's agent declared, which
	// is what decides whether the run also owes the `rag` measure. It is the
	// declared set rather than the resolved one on purpose: "could this run
	// retrieve" is the question the platform meters, and a knowledge base that
	// failed to resolve would not change that answer.
	KnowledgeBases int
	// Usage is what the run spent.
	Usage usage.Totals
}

// Reporter reports a finished run's usage.
//
// A nil Reporter reports nothing, which is the state of a deployment that has
// not configured an endpoint — the same "absent capability" shape the other
// optional dependencies use, so a caller needs no switch of its own.
type Reporter interface {
	Report(ctx context.Context, r Record)
}

// Client is the HTTP reporter.
type Client struct {
	timeout time.Duration
	client  *usageclient.Client
}

// New builds a reporter for the given endpoint, or nil when it is empty.
//
// A token is required with an endpoint: OneX refuses the route without it, so an
// endpoint without a token is a configuration that would fail on every run and
// say so only in a log line nobody reads. Answering nil here makes the
// deployment's own startup validation the place that catches it.
func New(endpoint, token string, timeout time.Duration) *Client {
	if endpoint == "" || token == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		timeout: timeout,
		client:  usageclient.New(endpoint, token, timeout),
	}
}

// reportBody is the wire shape. The field names are OneX's
// `agentctl.RecordUsageRequest`, which mirrors this service's own usage event so
// that a reader comparing the two sees the same words.
type reportBody struct {
	UserID         string    `json:"userID"`
	SessionKey     string    `json:"sessionKey"`
	RunID          string    `json:"runID"`
	KnowledgeBases int       `json:"knowledgeBases"`
	Usage          usageBody `json:"usage"`
}

type usageBody struct {
	PromptTokens     int64 `json:"promptTokens"`
	CompletionTokens int64 `json:"completionTokens"`
	Calls            int64 `json:"calls"`
	CacheHitTokens   int64 `json:"cacheHitTokens"`
	CacheMissTokens  int64 `json:"cacheMissTokens"`
}

// Report sends one run's usage and never returns an error: see the package note
// on why a bookkeeping failure must not reach the answer.
//
// A body with no user is not sent. An unidentifiable asker is the one case where
// the platform has nothing to key the row by, and posting it would write usage
// no account can read back — a row that is both unattributable and unreadable,
// which is worse than the log line this leaves.
func (c *Client) Report(ctx context.Context, r Record) {
	if c == nil {
		return
	}
	if r.UserID == "" {
		slog.InfoContext(ctx, "usage report skipped: the run has no identified asker",
			"run", r.RunID, "session", r.SessionKey)
		return
	}

	body, err := json.Marshal(reportBody{
		UserID:         r.UserID,
		SessionKey:     r.SessionKey,
		RunID:          r.RunID,
		KnowledgeBases: r.KnowledgeBases,
		Usage: usageBody{
			PromptTokens:     int64(r.Usage.PromptTokens),
			CompletionTokens: int64(r.Usage.CompletionTokens),
			Calls:            int64(r.Usage.Calls),
			CacheHitTokens:   int64(r.Usage.CacheHitTokens),
			CacheMissTokens:  int64(r.Usage.CacheMissTokens),
		},
	})
	if err != nil {
		slog.ErrorContext(ctx, "usage report could not be encoded", "run", r.RunID, "error", err.Error())
		return
	}

	// A context of its own: the run's context is cancelled the moment the run
	// ends, and reporting on it would fail every time — which would look exactly
	// like an unreachable endpoint.
	reqCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
	defer cancel()

	status, respBody, err := c.client.Post(reqCtx, PathRecordUsage, body)
	if err != nil {
		slog.ErrorContext(ctx, "usage report failed; the run's usage is missing from the ledger",
			"run", r.RunID, "user", r.UserID, "error", err.Error())
		return
	}
	if status < 200 || status >= 300 {
		slog.ErrorContext(ctx, "usage report was refused; the run's usage is missing from the ledger",
			"run", r.RunID, "user", r.UserID, "status", status, "error", string(respBody))
		return
	}

	var out struct {
		Rows int `json:"rows"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		// The rows were written; only the answer to "how many" was unreadable.
		// Saying so is better than reporting a failure that did not happen.
		slog.WarnContext(ctx, "usage report succeeded but its answer was unreadable",
			"run", r.RunID, "error", err.Error())
		return
	}
	slog.DebugContext(ctx, "usage reported", "run", r.RunID, "user", r.UserID, "rows", out.Rows)
}
