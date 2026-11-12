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

// Package agentrun is the Job provider that executes an agent run through
// oai-agent-apiserver's asynchronous run API. It is the backend ScheduledRun is
// migrated to: the Job controller owns retries and deadlines, while this
// provider owns the dispatch/cancel/status translation.
package agentrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/onexstack/onexai/internal/controller/job/job/providers/registry"
	"github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
)

// Type is the Job type this provider handles.
const Type v1beta1.JobType = v1beta1.JobTypeAgentRun

// A run reports terminal success in two vocabularies: the admission row (what
// POST /v1/agent/runs returns, and what the management API normalizes to)
// spells it "succeeded", while GET /v1/agent/runs/:id returns the durable run
// row, whose success spelling is "completed". Both settle the Job.
const (
	runStateSucceeded   = "succeeded"
	runStateCompleted   = "completed"
	runStateFailed      = "failed"
	runStateCanceled    = "canceled"
	runStateInterrupted = "interrupted"
)

func runSucceeded(state string) bool {
	return state == runStateSucceeded || state == runStateCompleted
}

// runNotFoundGrace is how long a 404 from GetRun is treated as "not visible
// yet" rather than "gone". Admission and execution are separate writes, so a
// poll right after Start can briefly race the run row.
const runNotFoundGrace = 30 * time.Second

// Provider dispatches Jobs to the agent run API.
type Provider struct {
	client registry.AgentRunClient
}

var _ registry.Provider = (*Provider)(nil)

// New returns a Provider wired to the given client.
func New(c registry.AgentRunClient) *Provider { return &Provider{client: c} }

func init() {
	registry.Register(Type, func(deps registry.Deps) registry.Provider {
		return New(deps.AgentRun)
	})
}

// Start submits the run. The dedup key makes a retried Start idempotent: the
// agent service returns the existing run instead of starting a second one.
func (p *Provider) Start(ctx context.Context, job *v1beta1.Job) error {
	var spec v1beta1.AgentRunProviderSpec
	if err := registry.DecodeSpec(job, &spec); err != nil {
		return err
	}
	if spec.Agent == "" || spec.Prompt == "" {
		return fmt.Errorf("agentrun job %s/%s: agent and prompt are required", job.Namespace, job.Name)
	}
	if spec.SubjectID == "" {
		return fmt.Errorf("agentrun job %s/%s: subjectID is required", job.Namespace, job.Name)
	}
	if p.client == nil {
		return fmt.Errorf("agentrun job %s/%s: agent run client is not configured", job.Namespace, job.Name)
	}

	timeout := int64(0)
	if spec.TimeoutSeconds != nil {
		timeout = *spec.TimeoutSeconds
	} else if job.Spec.ActiveDeadlineSeconds != nil {
		timeout = *job.Spec.ActiveDeadlineSeconds
	}

	scheduleRef, trigger, scheduledTime := schedulerContext(job)
	metadata := metadataFor(job)
	metadata["trigger"] = trigger
	metadata["scheduledTime"] = strconv.FormatInt(scheduledTime, 10)

	result, err := p.client.CreateRun(ctx, &registry.AgentRunRequest{
		DedupKey:         dedupKey(job),
		Agent:            spec.Agent,
		Prompt:           spec.Prompt,
		SubjectID:        spec.SubjectID,
		SubjectName:      spec.SubjectName,
		Metering:         defaultString(spec.Metering, "none"),
		ApprovalFallback: defaultString(spec.ApprovalFallback, "deny"),
		SessionPolicy:    defaultString(spec.SessionPolicy, "new"),
		Source:           defaultString(spec.Source, "scheduled-run"),
		Trigger:          trigger,
		ScheduleRef:      scheduleRef,
		ScheduledTime:    scheduledTime,
		Attempt:          job.Status.Attempt,
		TimeoutSeconds:   timeout,
		Metadata:         metadata,
	})
	if err != nil {
		return err
	}

	return registry.EncodeStatus(job, &v1beta1.AgentRunProviderStatus{
		RunID:     result.ID,
		SessionID: result.SessionID,
		Attempt:   job.Status.Attempt,
		Phase:     result.State,
	})
}

// Status polls the run and translates its state into the provider contract.
func (p *Provider) Status(ctx context.Context, job *v1beta1.Job) (registry.Status, error) {
	var st v1beta1.AgentRunProviderStatus
	if err := registry.DecodeStatus(job, &st); err != nil {
		return registry.Status{}, err
	}
	// Start has not completed yet (or its status write is still in flight).
	if st.RunID == "" {
		return registry.Status{}, nil
	}
	if p.client == nil {
		return registry.Status{}, fmt.Errorf("agentrun job %s/%s: agent run client is not configured", job.Namespace, job.Name)
	}

	result, err := p.client.GetRun(ctx, st.RunID)
	if err != nil {
		// A run the service no longer knows (its session or row was lost, or it
		// was deleted) will never report again. Settle the Job instead of
		// polling a 404 forever — under Forbid that would wedge the schedule.
		// The grace window covers the moment between admission and the run row
		// becoming readable, where a 404 is transient.
		if errors.Is(err, registry.ErrRunNotFound) {
			if job.Status.StartedAt != nil && time.Since(job.Status.StartedAt.Time) < runNotFoundGrace {
				return registry.Status{}, nil
			}
			return registry.Status{
				Finished: true, Succeeded: false, Message: "agent run no longer exists",
			}, nil
		}
		return registry.Status{}, err
	}

	st.Phase = result.State
	st.Message = result.Message
	if err := registry.EncodeStatus(job, &st); err != nil {
		return registry.Status{}, err
	}

	switch {
	case runSucceeded(result.State):
		return registry.Status{Finished: true, Succeeded: true, Message: result.Message}, nil
	case result.State == runStateInterrupted:
		// An occurrence has no one to answer an interactive pause
		// (ask_user_question raises its own interrupt, which the approval
		// fallback does not cover). Left alone it holds the run — and, under
		// Forbid, the whole schedule — until the approval expires a day later.
		// Stop it and settle the Job with the reason.
		if err := p.client.CancelRun(ctx, st.RunID); err != nil {
			return registry.Status{}, err
		}
		return registry.Status{
			Finished: true, Succeeded: false,
			Message: "agent run is waiting for input a scheduled job cannot provide",
		}, nil
	case result.State == runStateFailed, result.State == runStateCanceled:
		return registry.Status{Finished: true, Succeeded: false, Message: result.Message}, nil
	default:
		return registry.Status{}, nil
	}
}

// Stop cancels the run. A Job that never started has nothing to cancel.
func (p *Provider) Stop(ctx context.Context, job *v1beta1.Job) error {
	var st v1beta1.AgentRunProviderStatus
	if err := registry.DecodeStatus(job, &st); err != nil {
		return err
	}
	if st.RunID == "" || p.client == nil {
		return nil
	}
	err := p.client.CancelRun(ctx, st.RunID)
	if errors.Is(err, registry.ErrRunNotFound) {
		// Already gone: the cancel achieved what it wanted, and returning the
		// error would keep the finalizer from ever releasing the Job.
		return nil
	}
	return err
}

// Children returns nil: a run is not a Kubernetes child object.
func (p *Provider) Children(context.Context, *v1beta1.Job) ([]client.Object, error) {
	return nil, nil
}

// dedupKey is stable across retries of the same attempt and changes when the
// controller increments Status.Attempt.
func dedupKey(job *v1beta1.Job) string {
	identity := string(job.UID)
	if identity == "" {
		identity = job.Namespace + "/" + job.Name
	}
	sum := sha256.Sum256([]byte(identity + "|" + strconv.Itoa(int(job.Status.Attempt))))
	return hex.EncodeToString(sum[:])
}

// schedulerContext derives the schedule name, trigger and planned time from
// the Job. A CronJob owns both scheduled and manual-trigger Jobs, so the
// manual annotation has to win over the owner reference.
func schedulerContext(job *v1beta1.Job) (scheduleRef, trigger string, scheduledTime int64) {
	if owner := metav1.GetControllerOf(job); owner != nil && owner.Kind == "CronJob" {
		scheduleRef = owner.Name
		trigger = "scheduled"
	} else {
		trigger = "api"
	}
	if job.Annotations["onexai.io/trigger"] == "manual" {
		trigger = "manual"
	}
	if raw := job.Annotations[v1beta1.CronJobScheduledTimestampAnnotation]; raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			scheduledTime = parsed.UnixMilli()
		}
	}
	return scheduleRef, trigger, scheduledTime
}

func metadataFor(job *v1beta1.Job) map[string]string {
	meta := map[string]string{
		"job":        job.Namespace + "/" + job.Name,
		"jobUID":     string(job.UID),
		"jobType":    string(job.Spec.Type),
		"generation": strconv.FormatInt(job.Generation, 10),
	}
	if owner := metav1.GetControllerOf(job); owner != nil {
		meta["cronjob"] = owner.Kind + "/" + owner.Name
	}
	return meta
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
