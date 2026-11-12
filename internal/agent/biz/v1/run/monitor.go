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

package run

import (
	"cmp"
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

const (
	// maxMonitorScanRows bounds the rows one monitoring query aggregates. The
	// store reads one extra row to tell "exactly at the cap" from "over it".
	maxMonitorScanRows  = 50_000
	monitorTopSlowLimit = 5
	// monitorMaxAgentGroups bounds the per-agent breakdown. Distinct agents are
	// normally far below this, but malformed or adversarial data could
	// otherwise turn the breakdown into an unbounded response.
	monitorMaxAgentGroups = 200
)

// AdminRunMonitorResponse is the aggregate monitoring view over a half-open
// time window. Agent is empty when the caller asked for the whole fleet
// (all=true); Agents then carries the per-agent breakdown.
type AdminRunMonitorResponse struct {
	Agent       string `json:"agent"`
	All         bool   `json:"all"`
	From        int64  `json:"from"`
	To          int64  `json:"to"`
	Step        int64  `json:"step"`
	GeneratedAt int64  `json:"generatedAt"`
	// Truncated reports that the window held more rows than the scan cap; the
	// numbers cover the newest cap rows only.
	Truncated bool `json:"truncated"`
	// ScannedRows and ScanLimit make the sample boundary explicit: ScannedRows
	// is the number of rows actually aggregated, ScanLimit the cap.
	ScannedRows int `json:"scannedRows"`
	ScanLimit   int `json:"scanLimit"`
	// AgentsTruncated reports that the per-agent breakdown was capped.
	AgentsTruncated      bool                    `json:"agentsTruncated"`
	Summary              MonitorSummary          `json:"summary"`
	Agents               []AgentMonitorSummary   `json:"agents"`
	Buckets              []MonitorBucket         `json:"buckets"`
	DurationDistribution []MonitorDurationBucket `json:"durationDistribution"`
	Triggers             []MonitorTriggerCount   `json:"triggers"`
	TopSlowRuns          []MonitorRunSummary     `json:"topSlowRuns"`
	RecentRuns           []MonitorRunSummary     `json:"recentRuns"`
}

// AgentMonitorSummary is one agent's share of the window.
type AgentMonitorSummary struct {
	Agent       string              `json:"agent"`
	Total       int                 `json:"total"`
	Succeeded   int                 `json:"succeeded"`
	Failed      int                 `json:"failed"`
	Canceled    int                 `json:"canceled"`
	Active      int                 `json:"active"`
	SuccessRate float64             `json:"successRate"`
	Duration    MonitorDurationStat `json:"duration"`
	Tokens      MonitorTokenStats   `json:"tokens"`
	// LastRunAt is the newest row's window anchor (created_at), 0 when the
	// agent had no rows.
	LastRunAt int64 `json:"lastRunAt"`
}

// MonitorSummary is the window total.
type MonitorSummary struct {
	Total       int                 `json:"total"`
	Succeeded   int                 `json:"succeeded"`
	Failed      int                 `json:"failed"`
	Canceled    int                 `json:"canceled"`
	Active      int                 `json:"active"`
	SuccessRate float64             `json:"successRate"`
	Duration    MonitorDurationStat `json:"duration"`
	Tokens      MonitorTokenStats   `json:"tokens"`
}

// MonitorDurationStat describes the terminal runs that have a usable duration.
// Samples distinguishes "no timed run" from "all runs took zero milliseconds".
type MonitorDurationStat struct {
	AvgMs   int64 `json:"avgMs"`
	P95Ms   int64 `json:"p95Ms"`
	MaxMs   int64 `json:"maxMs"`
	Samples int   `json:"samples"`
}

// MonitorTokenStats is the window's token usage. RunsWithUsage is the number of
// runs whose usage JSON could be read; CacheHitRate is zero when the upstream
// did not report the cache split.
type MonitorTokenStats struct {
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	ReasoningTokens  int64   `json:"reasoningTokens"`
	CacheHitTokens   int64   `json:"cacheHitTokens"`
	CacheMissTokens  int64   `json:"cacheMissTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	CacheHitRate     float64 `json:"cacheHitRate"`
	Calls            int64   `json:"calls"`
	RunsWithUsage    int     `json:"runsWithUsage"`
}

// MonitorBucket is one time bucket. Empty buckets are present with zero counts
// so the chart's x axis is contiguous.
type MonitorBucket struct {
	Start            int64 `json:"start"`
	End              int64 `json:"end"`
	Total            int   `json:"total"`
	Succeeded        int   `json:"succeeded"`
	Failed           int   `json:"failed"`
	Canceled         int   `json:"canceled"`
	Active           int   `json:"active"`
	DurationSamples  int   `json:"durationSamples"`
	AvgMs            int64 `json:"avgMs"`
	P95Ms            int64 `json:"p95Ms"`
	MaxMs            int64 `json:"maxMs"`
	PromptTokens     int64 `json:"promptTokens"`
	CompletionTokens int64 `json:"completionTokens"`
	TotalTokens      int64 `json:"totalTokens"`
}

// MonitorDurationBucket is one fixed duration class.
type MonitorDurationBucket struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// MonitorTriggerCount is one trigger's share of the window.
type MonitorTriggerCount struct {
	Trigger string `json:"trigger"`
	Count   int    `json:"count"`
}

// MonitorRunSummary is the per-run shape the recent and slowest lists use.
//
// Agent and Source identify the row in a fleet view: an interactive run has no
// admission, so without them a mixed list would claim every row came from the
// same place.
type MonitorRunSummary struct {
	ID          string `json:"id"`
	Agent       string `json:"agent"`
	Source      string `json:"source"`
	State       string `json:"state"`
	Trigger     string `json:"trigger"`
	StartedAt   int64  `json:"startedAt"`
	FinishedAt  int64  `json:"finishedAt"`
	CreatedAt   int64  `json:"createdAt"`
	DurationMs  int64  `json:"durationMs"`
	TotalTokens int64  `json:"totalTokens"`
}

// monitorDurationClasses is the fixed, ordered histogram vocabulary.
var monitorDurationClasses = []struct {
	key       string
	maxMillis int64
}{
	{key: "lt1s", maxMillis: 1_000},
	{key: "1sTo5s", maxMillis: 5_000},
	{key: "5sTo30s", maxMillis: 30_000},
	{key: "30sTo2m", maxMillis: 120_000},
	{key: "gte2m", maxMillis: 0}, // open-ended
}

// bucketAcc carries the non-serialized duration samples of one bucket.
type bucketAcc struct {
	bucket    MonitorBucket
	durations []int64
}

// agentAcc is the per-agent accumulator behind the fleet breakdown. The
// duration samples are kept out of the JSON and finalized once the scan ends.
type agentAcc struct {
	summary   AgentMonitorSummary
	durations []int64
}

// ListAdminRunMonitor aggregates runs over [from, to).
//
// With req.All the window covers the whole fleet and Agents carries the
// per-agent breakdown; otherwise req.Agent names one agent and Agents has at
// most one entry.
//
// The window covers both halves of an agent's history: asynchronous admissions
// (attributed by admission creation time, so a pending run that has no engine
// row yet still counts toward the total and the active count) and interactive
// runs recorded by the engine without an admission (attributed by start time).
// Duration and usage come from the joined run row.
func (b *runBiz) ListAdminRunMonitor(
	ctx context.Context, req *v1.ListAdminRunMonitorQuery,
) (*AdminRunMonitorResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}

	step := v1.ResolveMonitorStep(req.Step, req.To-req.From)
	limit := req.Limit
	if limit <= 0 {
		limit = v1.MonitorDefaultLimit
	}
	if limit > v1.MonitorMaxLimit {
		limit = v1.MonitorMaxLimit
	}

	rows, err := b.store.ListAgentRunWindow(ctx, runstore.AgentRunWindowFilter{
		Agent:     req.Agent,
		From:      req.From,
		To:        req.To,
		ScanLimit: maxMonitorScanRows + 1,
	})
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}
	truncated := len(rows) > maxMonitorScanRows
	if truncated {
		rows = rows[:maxMonitorScanRows]
	}

	bucketCount := int((req.To - req.From + step - 1) / step)
	accs := make([]bucketAcc, bucketCount)
	for i := range accs {
		start := req.From + int64(i)*step
		accs[i].bucket.Start = start
		accs[i].bucket.End = start + step
	}

	summary := MonitorSummary{}
	durationCounts := make([]int, len(monitorDurationClasses))
	var allDurations []int64
	triggerCounts := make(map[string]int)
	agentAccs := make(map[string]*agentAcc)
	slowCandidates := make([]MonitorRunSummary, 0, len(rows))
	recentRuns := make([]MonitorRunSummary, 0, limit)

	for _, row := range rows {
		if row.CreatedAt < req.From || row.CreatedAt >= req.To {
			continue
		}
		index := int((row.CreatedAt - req.From) / step)
		if index < 0 || index >= len(accs) {
			continue
		}

		state := normalizeRunState(cmp.Or(row.State, row.RunState))
		bucketState := monitorBucketState(state)
		trigger := monitorTrigger(row.Trigger, row.ScheduleRef)
		duration, timed := runDuration(row)
		usage := decodeUsage(row.UsageJSON)
		rowTokens := usageTotal(usage)

		acc := &accs[index]
		agent := agentAccs[row.Agent]
		if agent == nil {
			agent = &agentAcc{summary: AgentMonitorSummary{Agent: row.Agent}}
			agentAccs[row.Agent] = agent
		}
		if row.CreatedAt > agent.summary.LastRunAt {
			agent.summary.LastRunAt = row.CreatedAt
		}

		acc.bucket.Total++
		summary.Total++
		agent.summary.Total++
		switch bucketState {
		case model.RunStateSucceeded:
			acc.bucket.Succeeded++
			summary.Succeeded++
			agent.summary.Succeeded++
		case model.RunStateFailed:
			acc.bucket.Failed++
			summary.Failed++
			agent.summary.Failed++
		case model.RunStateCanceled:
			acc.bucket.Canceled++
			summary.Canceled++
			agent.summary.Canceled++
		default:
			acc.bucket.Active++
			summary.Active++
			agent.summary.Active++
		}
		triggerCounts[trigger]++

		if timed {
			acc.durations = append(acc.durations, duration)
			agent.durations = append(agent.durations, duration)
			allDurations = append(allDurations, duration)
			count := durationClassIndex(duration)
			durationCounts[count]++
			entry := runSummaryOf(row, state, trigger, duration, rowTokens)
			slowCandidates = append(slowCandidates, entry)
			if len(recentRuns) < limit {
				recentRuns = append(recentRuns, entry)
			}
		} else if len(recentRuns) < limit {
			recentRuns = append(recentRuns, runSummaryOf(row, state, trigger, 0, rowTokens))
		}

		if usage != nil {
			summary.Tokens.RunsWithUsage++
			addUsage(&summary.Tokens, usage)
			addBucketUsage(&acc.bucket, usage)
			agent.summary.Tokens.RunsWithUsage++
			addUsage(&agent.summary.Tokens, usage)
		}
	}

	// Bucket durations.
	for i := range accs {
		finalizeDuration(&accs[i].durations)
		durations := accs[i].durations
		accs[i].bucket.DurationSamples = len(durations)
		if len(durations) > 0 {
			accs[i].bucket.AvgMs = average(durations)
			accs[i].bucket.P95Ms = percentileNearestRank(durations, 95)
			accs[i].bucket.MaxMs = durations[len(durations)-1]
		}
	}

	buckets := make([]MonitorBucket, len(accs))
	for i := range accs {
		buckets[i] = accs[i].bucket
	}

	sort.Slice(allDurations, func(i, j int) bool { return allDurations[i] < allDurations[j] })
	summary.Duration.Samples = len(allDurations)
	if len(allDurations) > 0 {
		summary.Duration.AvgMs = average(allDurations)
		summary.Duration.P95Ms = percentileNearestRank(allDurations, 95)
		summary.Duration.MaxMs = allDurations[len(allDurations)-1]
	}
	summary.SuccessRate = ratio(int64(summary.Succeeded),
		int64(summary.Succeeded+summary.Failed+summary.Canceled))
	summary.Tokens.CacheHitRate = ratio(summary.Tokens.CacheHitTokens,
		summary.Tokens.CacheHitTokens+summary.Tokens.CacheMissTokens)

	// Per-agent breakdown. The durations are finalized here so the response
	// carries percentiles, not the sample slices that produced them.
	agents := make([]AgentMonitorSummary, 0, len(agentAccs))
	for _, acc := range agentAccs {
		finalizeDuration(&acc.durations)
		if len(acc.durations) > 0 {
			acc.summary.Duration.Samples = len(acc.durations)
			acc.summary.Duration.AvgMs = average(acc.durations)
			acc.summary.Duration.P95Ms = percentileNearestRank(acc.durations, 95)
			acc.summary.Duration.MaxMs = acc.durations[len(acc.durations)-1]
		}
		acc.summary.SuccessRate = ratio(int64(acc.summary.Succeeded),
			int64(acc.summary.Succeeded+acc.summary.Failed+acc.summary.Canceled))
		acc.summary.Tokens.CacheHitRate = ratio(acc.summary.Tokens.CacheHitTokens,
			acc.summary.Tokens.CacheHitTokens+acc.summary.Tokens.CacheMissTokens)
		agents = append(agents, acc.summary)
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Total != agents[j].Total {
			return agents[i].Total > agents[j].Total
		}
		return agents[i].Agent < agents[j].Agent
	})
	agentsTruncated := len(agents) > monitorMaxAgentGroups
	if agentsTruncated {
		agents = agents[:monitorMaxAgentGroups]
	}

	distribution := make([]MonitorDurationBucket, 0, len(monitorDurationClasses))
	for i, class := range monitorDurationClasses {
		distribution = append(distribution, MonitorDurationBucket{
			Key:   class.key,
			Count: durationCounts[i],
		})
	}

	triggers := make([]MonitorTriggerCount, 0, len(triggerCounts))
	for trigger, count := range triggerCounts {
		triggers = append(triggers, MonitorTriggerCount{Trigger: trigger, Count: count})
	}
	sort.Slice(triggers, func(i, j int) bool {
		if triggers[i].Count != triggers[j].Count {
			return triggers[i].Count > triggers[j].Count
		}
		return triggers[i].Trigger < triggers[j].Trigger
	})

	sort.Slice(slowCandidates, func(i, j int) bool {
		if slowCandidates[i].DurationMs != slowCandidates[j].DurationMs {
			return slowCandidates[i].DurationMs > slowCandidates[j].DurationMs
		}
		if slowCandidates[i].StartedAt != slowCandidates[j].StartedAt {
			return slowCandidates[i].StartedAt > slowCandidates[j].StartedAt
		}
		return slowCandidates[i].ID < slowCandidates[j].ID
	})
	if len(slowCandidates) > monitorTopSlowLimit {
		slowCandidates = slowCandidates[:monitorTopSlowLimit]
	}

	return &AdminRunMonitorResponse{
		Agent:                req.Agent,
		All:                  req.All,
		From:                 req.From,
		To:                   req.To,
		Step:                 step,
		GeneratedAt:          time.Now().UnixMilli(),
		Truncated:            truncated,
		ScannedRows:          len(rows),
		ScanLimit:            maxMonitorScanRows,
		AgentsTruncated:      agentsTruncated,
		Summary:              summary,
		Agents:               agents,
		Buckets:              buckets,
		DurationDistribution: distribution,
		Triggers:             triggers,
		TopSlowRuns:          slowCandidates,
		RecentRuns:           recentRuns,
	}, nil
}

// monitorBucketState maps both the admission and run vocabularies onto the four
// counters the bucket carries.
func monitorBucketState(state string) string {
	switch state {
	case model.RunStateSucceeded:
		return model.RunStateSucceeded
	case model.RunStateFailed:
		return model.RunStateFailed
	case model.RunStateCanceled:
		return model.RunStateCanceled
	default:
		return "active"
	}
}

func monitorTrigger(trigger, scheduleRef string) string {
	trigger = strings.TrimSpace(trigger)
	if trigger != "" {
		return trigger
	}
	if strings.TrimSpace(scheduleRef) != "" {
		return "scheduled"
	}
	return "unknown"
}

func runDuration(row runstore.MonitorRunRow) (int64, bool) {
	if row.StartedAt <= 0 || row.FinishedAt <= row.StartedAt {
		return 0, false
	}
	return row.FinishedAt - row.StartedAt, true
}

func runSummaryOf(
	row runstore.MonitorRunRow, state, trigger string, duration, totalTokens int64,
) MonitorRunSummary {
	return MonitorRunSummary{
		ID:          row.ID,
		Agent:       row.Agent,
		Source:      row.Source,
		State:       state,
		Trigger:     trigger,
		StartedAt:   row.StartedAt,
		FinishedAt:  row.FinishedAt,
		CreatedAt:   row.CreatedAt,
		DurationMs:  duration,
		TotalTokens: totalTokens,
	}
}

// usageTotal is the same fallback addUsage applies: an upstream that reports
// only prompt/completion still gets a total.
func usageTotal(usage *agentapi.Usage) int64 {
	if usage == nil {
		return 0
	}
	if usage.TotalTokens > 0 {
		return int64(usage.TotalTokens)
	}
	return int64(usage.PromptTokens + usage.CompletionTokens)
}

func addUsage(stats *MonitorTokenStats, usage *agentapi.Usage) {
	stats.PromptTokens += int64(usage.PromptTokens)
	stats.CompletionTokens += int64(usage.CompletionTokens)
	stats.TotalTokens += usageTotal(usage)
	stats.ReasoningTokens += int64(usage.ReasoningTokens)
	stats.CacheHitTokens += int64(usage.CacheHitTokens)
	stats.CacheMissTokens += int64(usage.CacheMissTokens)
	stats.CacheWriteTokens += int64(usage.CacheWriteTokens)
	stats.Calls += int64(usage.Calls)
}

func addBucketUsage(bucket *MonitorBucket, usage *agentapi.Usage) {
	bucket.PromptTokens += int64(usage.PromptTokens)
	bucket.CompletionTokens += int64(usage.CompletionTokens)
	bucket.TotalTokens += usageTotal(usage)
}

// decodeUsage reads the PascalCase usage JSON a settled run stores. Empty,
// null, malformed and all-zero payloads are treated as "no usage": the run
// either did not reach a model call or the upstream reported nothing.
func decodeUsage(raw string) *agentapi.Usage {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var usage agentapi.Usage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		return nil
	}
	if usage == (agentapi.Usage{}) {
		return nil
	}
	return &usage
}

func durationClassIndex(durationMs int64) int {
	for i, class := range monitorDurationClasses {
		if class.maxMillis == 0 || durationMs < class.maxMillis {
			return i
		}
	}
	return len(monitorDurationClasses) - 1
}

func finalizeDuration(durations *[]int64) {
	sort.Slice(*durations, func(i, j int) bool { return (*durations)[i] < (*durations)[j] })
}

func average(sorted []int64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	var total int64
	for _, value := range sorted {
		total += value
	}
	return total / int64(len(sorted))
}

// percentileNearestRank returns the nearest-rank percentile of a sorted slice.
// It does not interpolate: with four samples the 95th percentile is the
// largest, which is what an operator reading a latency chart expects.
func percentileNearestRank(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (len(sorted)*p + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func ratio(numerator, denominator int64) float64 {
	if denominator == 0 {
		return 0
	}
	return math.Round(float64(numerator)/float64(denominator)*10000) / 10000
}
