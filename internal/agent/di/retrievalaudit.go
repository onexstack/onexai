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

package di

import (
	"context"
	"log/slog"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/kbtool"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// retrievalAuditBuffer is how many searches may wait for a database insert.
//
// It is a buffer, not a queue: a full buffer drops an entry and logs it. A
// retrieval must not wait on the audit, because the audit is an after-the-fact
// aid and the answer is the product. Dropping is visible in the log; blocking
// would be visible only as latency.
const retrievalAuditBuffer = 256

// retrievalAuditWriteTimeout bounds one insert. The writer runs on its own
// goroutine, so a slow database costs the oldest buffered entries rather than
// the request.
const retrievalAuditWriteTimeout = 3 * time.Second

// newRetrievalAudit returns the writer the retrieval tool calls, or nil when
// there is no database (tests and deployments without a store).
func newRetrievalAudit(audit agentstore.RetrievalAuditStore) kbtool.AuditFunc {
	if audit == nil {
		return nil
	}
	queue := make(chan model.RetrievalAuditM, retrievalAuditBuffer)
	go func() {
		for row := range queue {
			ctx, cancel := context.WithTimeout(context.Background(), retrievalAuditWriteTimeout)
			if err := audit.Create(ctx, &row); err != nil {
				slog.Warn("retrieval audit: write failed", "error", err, "trace_id", row.TraceID)
			}
			cancel()
		}
	}()

	return func(e kbtool.AuditEntry) {
		select {
		case queue <- model.RetrievalAuditM{
			UserID:         e.UserID,
			Agent:          e.Agent,
			KnowledgeBases: e.KnowledgeBases,
			Documents:      e.Documents,
			Passages:       e.Passages,
			Bytes:          e.Bytes,
			TraceID:        e.TraceID,
		}:
		default:
			slog.Warn("retrieval audit: buffer full, entry dropped",
				"user_id", e.UserID, "trace_id", e.TraceID, "documents", e.Documents)
		}
	}
}
