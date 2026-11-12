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

// This file holds the request shapes for endpoints whose parameters are bound
// from a path, a query or an external tool's argument names rather than from a
// generated message. Their names carry a Query/Params suffix because the
// generated messages occupy the Request names and model fewer binding fields.
// They live beside the generated API types so the handler and the biz layer
// exchange the same currency.

package v1

import "time"

// defaultShareTTLSeconds is how long a created share grants read access.
const defaultShareTTLSeconds = 7 * 24 * 60 * 60

// Monitoring window and bucket bounds.
const (
	// MonitorStep1m is the finest bucket the management monitor accepts. A
	// finer step would turn a seven-day window into thousands of points the
	// drawer cannot render usefully.
	MonitorStep1m  int64 = 60_000
	MonitorStep5m  int64 = 300_000
	MonitorStep30m int64 = 1_800_000
	MonitorStep1h  int64 = 3_600_000

	// MonitorDefaultWindowMs is the window used when the caller omits both
	// bounds; it matches the drawer's default one-hour range.
	MonitorDefaultWindowMs int64 = 3_600_000
	// MonitorMaxWindowMs is seven days plus one bucket, so a client that
	// floors its from bound to a bucket boundary still fits.
	MonitorMaxWindowMs int64 = (7*24 + 1) * 3_600_000
	// MonitorMaxBuckets bounds the response size regardless of the step the
	// caller asks for.
	MonitorMaxBuckets = 600

	MonitorDefaultLimit = 10
	MonitorMaxLimit     = 50

	// StuckDefaultLimit and StuckMaxLimit bound the stuck-run query, which the
	// console uses to surface asynchronous work that never settles.
	StuckDefaultLimit = 50
	StuckMaxLimit     = 200
)

// ListAdminRunMonitorQuery is a monitoring window. Times are Unix milliseconds
// and the window is half-open: [From, To).
//
// Agent and All are mutually exclusive: a caller either names one agent or
// explicitly asks for the whole fleet. Requiring the fleet mode to be spelled
// out keeps an omitted agent from silently turning a per-agent query into a
// full-table scan.
type ListAdminRunMonitorQuery struct {
	Agent string `form:"agent"`
	All   bool   `form:"all"`
	From  int64  `form:"from"`
	To    int64  `form:"to"`
	Step  int64  `form:"step"`
	Limit int    `form:"limit"`
}

// ListAdminRunStuckQuery names the fleet (or one agent) whose stuck runs are
// listed. Limit bounds the result; the thresholds are fixed by the business
// layer and echoed in the response.
type ListAdminRunStuckQuery struct {
	Agent string `form:"agent"`
	Limit int    `form:"limit"`
}

// Default fills the omitted limit.
func (q *ListAdminRunStuckQuery) Default() {
	if q.Limit == 0 {
		q.Limit = StuckDefaultLimit
	}
}

// Default fills the omitted bounds and derives a bucket size from the span.
// Negative values are deliberately left for the validator to reject.
func (q *ListAdminRunMonitorQuery) Default() {
	if q.To == 0 {
		q.To = time.Now().UnixMilli()
	}
	if q.From == 0 {
		q.From = q.To - MonitorDefaultWindowMs
	}
	if q.Step == 0 {
		q.Step = ResolveMonitorStep(q.Step, q.To-q.From)
	}
	if q.Limit == 0 {
		q.Limit = MonitorDefaultLimit
	}
}

// ResolveMonitorStep returns the requested step when it is one of the supported
// sizes, and otherwise picks a size appropriate for the span. The mapping keeps
// every supported range at 168 points or fewer.
func ResolveMonitorStep(step, span int64) int64 {
	if IsMonitorStep(step) {
		return step
	}
	switch {
	case span <= 2*3_600_000:
		return MonitorStep1m
	case span <= 12*3_600_000:
		return MonitorStep5m
	case span <= 48*3_600_000:
		return MonitorStep30m
	default:
		return MonitorStep1h
	}
}

// IsMonitorStep reports whether step is one of the four supported bucket sizes.
func IsMonitorStep(step int64) bool {
	switch step {
	case MonitorStep1m, MonitorStep5m, MonitorStep30m, MonitorStep1h:
		return true
	default:
		return false
	}
}

// ListSessionRunQuery lists the runs of one conversation.
type ListSessionRunQuery struct {
	ID    string `uri:"id"`
	Limit int    `form:"limit"`
}

// ListRunEventQuery is the run-event query.
type ListRunEventQuery struct {
	ID    string `uri:"id"`
	After int64  `form:"after"`
}

// ListApprovalQuery lists pending approvals.
type ListApprovalQuery struct {
	Status string `form:"status"`
	Limit  int    `form:"limit"`
}

// ListAdminRunQuery lists asynchronous runs on the management surface.
//
// From and To are optional Unix-millisecond bounds on the admission's
// created_at (half-open [From, To)). They exist so the observability failure
// view can show the same window as the run monitor; both must be supplied
// together.
type ListAdminRunQuery struct {
	Offset      int    `form:"offset"`
	Limit       int    `form:"limit"`
	State       string `form:"state"`
	Agent       string `form:"agent"`
	Trigger     string `form:"trigger"`
	Source      string `form:"source"`
	ScheduleRef string `form:"scheduleRef"`
	SessionID   string `form:"sessionId"`
	Keyword     string `form:"keyword"`
	From        int64  `form:"from"`
	To          int64  `form:"to"`
}

// GetAdminRunRequest names one asynchronous run.
type GetAdminRunRequest struct {
	ID string `uri:"id"`
}

// ListAdminRunEventQuery pages through one run's events.
type ListAdminRunEventQuery struct {
	ID    string `uri:"id"`
	After int64  `form:"after"`
	Limit int    `form:"limit"`
}

// CancelAdminRunRequest names the run to stop.
type CancelAdminRunRequest struct {
	ID string `uri:"id"`
}

// RetryAdminRunRequest names the terminal run to replay.
type RetryAdminRunRequest struct {
	ID string `uri:"id"`
}

// ListRevisionQuery lists the revisions an agent has executed.
type ListRevisionQuery struct {
	Agent string `form:"agent"`
	Limit int    `form:"limit"`
}

// GetRevisionParams names one revision.
type GetRevisionParams struct {
	ID string `uri:"id"`
}

// CreateSessionShareParams is the input to the share endpoint. TTLSeconds is a
// pointer so an explicit zero can be refused rather than read as "unset".
type CreateSessionShareParams struct {
	ID         string `uri:"id"`
	TTLSeconds *int   `form:"ttlSeconds"`
}

// Default fills the share lifetime. It runs before validation, so an explicit
// zero is still visible to the validator and is refused; only an absent value
// gets the default.
func (rq *CreateSessionShareParams) Default() {
	if rq.TTLSeconds == nil {
		value := defaultShareTTLSeconds
		rq.TTLSeconds = &value
	}
}

// SharedRunParams names a run reached through a share token.
type SharedRunParams struct {
	ID string `uri:"id"`
}

// SharedRunEventsQuery is the shared run-event query.
type SharedRunEventsQuery struct {
	ID    string `uri:"id"`
	After int64  `form:"after"`
}

// SharedSessionRunsParams names a session reached through a share token.
type SharedSessionRunsParams struct {
	ID string `uri:"id"`
}

// ListScheduledExecutionQuery lists the executions of one schedule.
type ListScheduledExecutionQuery struct {
	Name      string `uri:"name"`
	Namespace string `form:"namespace"`
	Limit     int    `form:"limit"`
}

// GetLogsParams mirrors the argument names of the external log tool.
type GetLogsParams struct {
	AccountID    string `json:"account_id"`
	ClusterName  string `json:"cluster_name"`
	Namespace    string `json:"namespace"`
	PodName      string `json:"pod_name"`
	Container    string `json:"container,omitempty"`
	Previous     *bool  `json:"previous,omitempty"`
	SinceSeconds *int   `json:"since_seconds,omitempty"`
	TailLines    *int   `json:"tail_lines,omitempty"`
}

// ListK8sEventParams mirrors the argument names of the external event tool.
type ListK8sEventParams struct {
	AccountID          string `json:"account_id"`
	ClusterName        string `json:"cluster_name"`
	Namespace          string `json:"namespace"`
	InvolvedObjectKind string `json:"involved_object_kind,omitempty"`
	InvolvedObjectName string `json:"involved_object_name,omitempty"`
	Limit              *int   `json:"limit,omitempty"`
}
