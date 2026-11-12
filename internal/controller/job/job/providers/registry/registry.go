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

// Package registry provides the pluggable provider registry used by the batch
// Job controller. Providers register a Factory for a Job type and the controller
// resolves the concrete backend via Build.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
)

// Status is a provider's view of the job's current execution state.
type Status struct {
	// Finished is true when the provider workload has reached a terminal state.
	Finished bool
	// Succeeded is true when the provider workload finished successfully.
	Succeeded bool
	// Message is an optional human-readable detail of the current/final state.
	Message string
}

// AgentRunRequest is the provider-neutral request an AgentRunClient submits.
// Keeping it here rather than in the client package is what lets the provider
// be tested without an HTTP server.
type AgentRunRequest struct {
	DedupKey         string
	Agent            string
	Prompt           string
	SubjectID        string
	SubjectName      string
	Metering         string
	ApprovalFallback string
	SessionPolicy    string
	Source           string
	Trigger          string
	ScheduleRef      string
	ScheduledTime    int64
	Attempt          int32
	TimeoutSeconds   int64
	Metadata         map[string]string
}

// AgentRunResult is the subset of a run the provider needs.
type AgentRunResult struct {
	ID        string
	SessionID string
	State     string
	Message   string
}

// ErrRunNotFound reports that the agent service no longer knows the run. It is
// separate from other errors because it is terminal for a Job: a run that
// disappeared will not come back, so a provider that kept polling would leave
// the Job (and, under Forbid, its whole schedule) stuck forever.
var ErrRunNotFound = errors.New("agent run not found")

// AgentRunClient is the asynchronous agent run API the agentrun provider uses.
type AgentRunClient interface {
	CreateRun(ctx context.Context, req *AgentRunRequest) (*AgentRunResult, error)
	GetRun(ctx context.Context, id string) (*AgentRunResult, error)
	CancelRun(ctx context.Context, id string) error
}

// Deps carries everything a provider factory may need.
type Deps struct {
	Client       client.Client
	AgentRun     AgentRunClient
	PollInterval time.Duration
}

// Provider abstracts the workload backend that actually executes a Job.
type Provider interface {
	// Start dispatches the job and returns an error if dispatch failed.
	Start(ctx context.Context, job *v1beta1.Job) error
	// Stop cancels a running workload.
	Stop(ctx context.Context, job *v1beta1.Job) error
	// Status returns the provider's view of the job's current state.
	Status(ctx context.Context, job *v1beta1.Job) (Status, error)
	// Children lists provider-managed child objects owned by the job.
	Children(ctx context.Context, job *v1beta1.Job) ([]client.Object, error)
}

// Factory constructs a Provider from the shared dependencies.
type Factory func(Deps) Provider

var (
	mu        sync.Mutex
	factories = map[v1beta1.JobType]Factory{}
)

// Register registers a provider factory for a Job type. It panics on a
// duplicate, which is a programming error caught at startup.
func Register(t v1beta1.JobType, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := factories[t]; ok {
		panic("duplicate provider entry: " + string(t))
	}
	factories[t] = f
}

// Build constructs every registered provider and returns them keyed by type.
func Build(deps Deps) map[v1beta1.JobType]Provider {
	mu.Lock()
	defer mu.Unlock()

	out := make(map[v1beta1.JobType]Provider, len(factories))
	for t, f := range factories {
		out[t] = f(deps)
	}
	return out
}

// DecodeSpec decodes Job.Spec.ProviderSpec into out.
func DecodeSpec(job *v1beta1.Job, out any) error {
	if job.Spec.ProviderSpec == nil || len(job.Spec.ProviderSpec.Raw) == 0 {
		return fmt.Errorf("job %s/%s has no providerSpec", job.Namespace, job.Name)
	}
	if err := json.Unmarshal(job.Spec.ProviderSpec.Raw, out); err != nil {
		return fmt.Errorf("decode providerSpec of job %s/%s: %w", job.Namespace, job.Name, err)
	}
	return nil
}

// DecodeStatus decodes Job.Status.ProviderStatus into out. A missing status is
// not an error: it is the normal state of a job that has not started.
func DecodeStatus(job *v1beta1.Job, out any) error {
	if job.Status.ProviderStatus == nil || len(job.Status.ProviderStatus.Raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(job.Status.ProviderStatus.Raw, out); err != nil {
		return fmt.Errorf("decode providerStatus of job %s/%s: %w", job.Namespace, job.Name, err)
	}
	return nil
}

// EncodeStatus writes in into Job.Status.ProviderStatus.
func EncodeStatus(job *v1beta1.Job, in any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode providerStatus of job %s/%s: %w", job.Namespace, job.Name, err)
	}
	job.Status.ProviderStatus = &runtime.RawExtension{Raw: raw}
	return nil
}
