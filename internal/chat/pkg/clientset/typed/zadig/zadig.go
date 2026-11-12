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

// Package zadig provides a typed client for the Zadig DevOps platform's
// OpenAPI, used by the /pipeline command to trigger custom workflows.
//
// Authentication is a long-lived API token (Zadig: Account Settings -> API Token) sent
// as "Authorization: Bearer <token>"; there is no refresh flow and the token
// must never be logged or echoed in an error message.
//
// Triggering a custom workflow is not a "run workflow X" call that takes the
// definition for granted. The request names the jobs the task will consist of,
// and Zadig composes the task from those names alone:
//
//	inputs[i].job_name    the job to include in the task
//	inputs[i].job_type    its type (zadig-build / zadig-deploy / ...)
//	inputs[i].parameters  the values that job type's input takes
//
// So the selection is expressed by presence: a job left out of inputs does not
// run at all, and there is no skip flag to set. Two of the parameter objects are
// mandatory in practice, both read off Zadig's own handling
// (microservice/aslan/core/workflow/service/workflow/types.go):
//
//   - a zadig-build job must be given "registry" as "<address>/<namespace>";
//     a registry id, or an empty string, is rejected with "didn't find the
//     specified image registry". Its "service_list" names the services to build
//     and is matched on service_name AND service_module, so a list that matches
//     nothing builds nothing rather than everything.
//   - a zadig-deploy job must be given "env_name" and "service_list". The list
//     *is* the deployment's service selection rather than an override: Zadig
//     replaces the job's own services with it, so an input that names no
//     service is refused outright ("deploy service is empty for env ...")
//     instead of falling back to anything. For a job fed by a build job
//     (source: fromjob) each service's image is read from that build job's
//     $IMAGE output, so the list has to be exactly the build job's services —
//     an input's own "image_name" is not used on that path.
package zadig

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"resty.dev/v3"

	baseclientset "github.com/onexstack/onexai/internal/pkg/clientset"
)

const (
	// customTaskPath creates a task for a custom workflow.
	customTaskPath = "/openapi/workflows/custom/task"

	// customDetailPath is the custom workflow definition: the stages and jobs
	// that make up the workflow, each job with its spec. It takes the workflow
	// key in the path and the project in ?projectKey=.
	customDetailPath = "/openapi/workflows/custom/%s/detail"

	// registryPath lists the platform's image registries. A build job's input
	// has to name its registry as "<address>/<namespace>", never by id, so the
	// docker_registry_id a job spec carries is resolved through this list.
	registryPath = "/openapi/system/registry"

	// JobTypeBuild and JobTypeDeploy are Zadig's job type keys. They are what a
	// workflow's stages are made of, and what tells this client which job to
	// include for a given --stage.
	JobTypeBuild  = "zadig-build"
	JobTypeDeploy = "zadig-deploy"

	// JobSourceFromJob marks a deploy job whose services and images come from
	// another job's output (its spec's job_name) rather than from its own spec.
	JobSourceFromJob = "fromjob"

	// consolePipelineKind is the workflow-type segment of a task URL in Zadig's
	// web console: /v1/projects/detail/<project>/pipelines/<kind>/<workflow>/<taskID>.
	consolePipelineKind = "custom"
)

// Interface defines the Zadig client operations.
type Interface interface {
	// C, R and Healthz are the common typed-client surface shared by every
	// third-party client in this repository.
	baseclientset.Interface

	// WorkflowDetail reads a custom workflow's definition.
	WorkflowDetail(ctx context.Context, project, workflow string) (*WorkflowDetail, error)
	// RegistryAddress resolves a registry id to the "<address>/<namespace>"
	// string a build job's input must carry.
	RegistryAddress(ctx context.Context, registryID string) (string, error)
	// ExecuteCustomWorkflow creates a task for a custom workflow and returns it.
	ExecuteCustomWorkflow(ctx context.Context, req *ExecuteCustomWorkflowRequest) (*ExecuteWorkflowTask, error)
	// Configured reports whether an endpoint was configured at all. It exists so
	// a command can refuse with "Zadig 未配置" (Zadig is not configured) before asking the operator to
	// confirm a trigger that could only fail.
	Configured() bool
	// ConsoleURL returns the web-console link to a workflow task.
	ConsoleURL(project, workflow string, taskID int64) string
	// DefaultProject returns the deployment-level default project key, or "" when
	// none is configured. A deployment sets it once so an operator does not have
	// to pass --project on every trigger.
	DefaultProject() string
}

// WorkflowDetail is a custom workflow's definition as the OpenAPI returns it.
type WorkflowDetail struct {
	ProjectKey  string  `json:"project_key"`
	WorkflowKey string  `json:"workflow_key"`
	Stages      []Stage `json:"stages"`
}

// Stage is one stage of a workflow, holding the jobs it runs in order.
type Stage struct {
	Name string `json:"name"`
	Jobs []Job  `json:"jobs"`
}

// Job is one job of a workflow: its name, its type, and the spec that type
// reads. Spec is kept raw because its shape depends on the type, and because the
// request has to hand a per-type subset of it back to Zadig.
type Job struct {
	Name string          `json:"name"`
	Type string          `json:"type"`
	Spec json.RawMessage `json:"spec"`
}

// Jobs flattens the stages' jobs in definition order.
func (d *WorkflowDetail) Jobs() []Job {
	if d == nil {
		return nil
	}
	var out []Job
	for _, stage := range d.Stages {
		out = append(out, stage.Jobs...)
	}
	return out
}

// BuildService is one entry of a build job's service_and_builds list. Both the
// name and the module are kept: Zadig matches an input service on the pair, so
// keeping only one of them would silently build nothing.
type BuildService struct {
	ServiceName   string `json:"service_name"`
	ServiceModule string `json:"service_module"`
	BuildName     string `json:"build_name"`
}

// jobSpec is the subset of a job's spec this client reads. Every field is
// optional: which of them are filled depends on the job's type, and an absent
// one means "this job has none" rather than "the workflow is broken".
type jobSpec struct {
	ServiceAndBuilds []BuildService `json:"service_and_builds"`
	DockerRegistryID string         `json:"docker_registry_id"`
	Env              string         `json:"env"`
	Source           string         `json:"source"`
	JobName          string         `json:"job_name"`
}

// spec decodes the job's spec, tolerating a missing or unreadable one: a job
// whose spec cannot be read is reported through the empty values its callers
// check for, not by failing the whole command.
func (j Job) spec() jobSpec {
	var s jobSpec
	if len(j.Spec) > 0 {
		_ = json.Unmarshal(j.Spec, &s)
	}
	return s
}

// BuildServices returns the services a build job is configured to build.
func (j Job) BuildServices() []BuildService { return j.spec().ServiceAndBuilds }

// RegistryID returns the id of the registry a build job pushes its images to.
func (j Job) RegistryID() string { return j.spec().DockerRegistryID }

// Env returns the environment a deploy job deploys to.
func (j Job) Env() string { return j.spec().Env }

// ImageSourceJob returns the name of the job a deploy job takes its services and
// images from, or "" when they do not come from another job.
func (j Job) ImageSourceJob() string {
	s := j.spec()
	if s.Source != JobSourceFromJob {
		return ""
	}
	return s.JobName
}

// ExecuteCustomWorkflowRequest describes one custom-workflow trigger.
type ExecuteCustomWorkflowRequest struct {
	// ProjectName is the Zadig project key the workflow belongs to.
	ProjectName string
	// WorkflowName is the custom workflow's name.
	WorkflowName string
	// Inputs names the jobs the created task consists of. Zadig builds the task
	// from these and drops every other job, so a caller that means "build only"
	// says so by leaving the deploy job out.
	Inputs []JobInput
}

// JobInput is one entry of the request's inputs array: a job to run, and the
// parameters its type's input takes. Parameters may be nil for a job type that
// needs none, which leaves that job's configuration exactly as the workflow has
// it.
type JobInput struct {
	JobName    string
	JobType    string
	Parameters map[string]any
}

// ExecuteWorkflowTask is the outcome of a successful trigger.
type ExecuteWorkflowTask struct {
	TaskID int64 `json:"task_id"`
	// Raw is Zadig's full response body, kept so a caller can surface fields this
	// struct does not model without a second round trip.
	Raw map[string]any `json:"-"`
}

// Client implements Interface over the Zadig OpenAPI.
type Client struct {
	// baseURL is kept for the web-console link; requests go through client.
	baseURL string
	project string

	client *baseclientset.Client
}

var _ Interface = (*Client)(nil)

// noopClient is returned when no endpoint is configured.
type noopClient struct{}

func (n *noopClient) WorkflowDetail(_ context.Context, _, _ string) (*WorkflowDetail, error) {
	return nil, fmt.Errorf("zadig: endpoint not configured")
}

func (n *noopClient) RegistryAddress(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("zadig: endpoint not configured")
}

func (n *noopClient) ExecuteCustomWorkflow(_ context.Context, _ *ExecuteCustomWorkflowRequest) (*ExecuteWorkflowTask, error) {
	return nil, fmt.Errorf("zadig: endpoint not configured")
}

func (n *noopClient) Configured() bool { return false }

func (n *noopClient) ConsoleURL(_, _ string, _ int64) string { return "" }

func (n *noopClient) DefaultProject() string { return "" }

// C, R and Healthz keep the no-op client on the same surface as a configured
// one. The resty client is detached: nothing that reaches it is a Zadig call.
func (n *noopClient) C() *resty.Client { return resty.New() }

func (n *noopClient) R(ctx context.Context) *resty.Request { return resty.New().R().SetContext(ctx) }

func (n *noopClient) Healthz(context.Context) error {
	return fmt.Errorf("zadig: endpoint not configured")
}

// NewClient creates a Zadig client. When endpoint is empty a no-op client is
// returned that fails every call with a clear error, so a deployment that
// configures no Zadig gets a readable message rather than a nil dereference.
//
// project is the deployment's default project key; it is a convenience for
// callers, not a credential, and is reported back through DefaultProject.
func NewClient(endpoint, token, project string) Interface {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return &noopClient{}
	}
	// Built literally rather than with NewRestyOptions: that helper turns on
	// three retries, and a workflow trigger is not safe to replay.
	opts := &genericoptions.RestyOptions{
		Endpoint: endpoint,
		Token:    strings.TrimSpace(token),
		Timeout:  30 * time.Second,
	}
	return &Client{
		baseURL: endpoint,
		project: strings.TrimSpace(project),
		client:  baseclientset.NewForConfig(opts),
	}
}

// C returns the underlying resty client.
func (c *Client) C() *resty.Client { return c.client.C() }

// R builds a request bound to ctx.
func (c *Client) R(ctx context.Context) *resty.Request { return c.client.R(ctx) }

// Healthz checks the Zadig endpoint.
func (c *Client) Healthz(ctx context.Context) error { return c.client.Healthz(ctx) }

// Configured reports whether an endpoint was configured.
func (c *Client) Configured() bool { return true }

// DefaultProject returns the configured default project key.
func (c *Client) DefaultProject() string { return c.project }

// WorkflowDetail reads a custom workflow's definition: its stages, its jobs, and
// each job's spec. The command reads it before triggering so that what it
// validates is what it sends, and so a mistyped workflow or service name is
// rejected here rather than by a task that ran the wrong thing.
func (c *Client) WorkflowDetail(ctx context.Context, project, workflow string) (*WorkflowDetail, error) {
	if strings.TrimSpace(project) == "" || strings.TrimSpace(workflow) == "" {
		return nil, fmt.Errorf("zadig: project and workflow are required")
	}

	path := fmt.Sprintf(customDetailPath, url.PathEscape(workflow))
	raw, status, err := c.get(ctx, path, url.Values{"projectKey": {project}})
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("zadig: read workflow %q failed: status=%d body=%s",
			workflow, status, baseclientset.Truncate(raw, 512))
	}

	var detail WorkflowDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		return nil, fmt.Errorf("zadig: read workflow %q: decode response (body=%s): %w",
			workflow, baseclientset.Truncate(raw, 512), err)
	}
	return &detail, nil
}

// RegistryAddress resolves a registry id to the "<address>/<namespace>" string a
// build job's input must carry.
//
// The separator is appended even when the namespace is empty, because that is
// the string Zadig matches the input against (addr + "/" + namespace): a value
// that reads better than the server's own key would not resolve.
func (c *Client) RegistryAddress(ctx context.Context, registryID string) (string, error) {
	id := registryID
	if id == "" {
		return "", fmt.Errorf("zadig: empty registry id")
	}

	raw, status, err := c.get(ctx, registryPath, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", fmt.Errorf("zadig: list registries failed: status=%d body=%s", status, baseclientset.Truncate(raw, 512))
	}

	var list []struct {
		ID        string `json:"registry_id"`
		Address   string `json:"address"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", fmt.Errorf("zadig: list registries: decode response (body=%s): %w", baseclientset.Truncate(raw, 512), err)
	}

	for _, reg := range list {
		if reg.ID != id {
			continue
		}
		if reg.Address == "" {
			return "", fmt.Errorf("zadig: registry %s has no address", id)
		}
		return reg.Address + "/" + reg.Namespace, nil
	}
	return "", fmt.Errorf("zadig: registry %s is not among the %d registries known to this Zadig", id, len(list))
}

// CustomTaskBody returns the exact JSON body ExecuteCustomWorkflow would send
// for req. It is exported so a caller can show the operator what it is about to
// send: the selection is carried entirely by which jobs appear here, so this is
// where a wrong idea about "build only" becomes visible instead of silently
// deploying something nobody asked for.
func CustomTaskBody(req *ExecuteCustomWorkflowRequest) map[string]any {
	if req == nil {
		return nil
	}

	inputs := make([]map[string]any, 0, len(req.Inputs))
	for _, in := range req.Inputs {
		entry := map[string]any{
			"job_name": in.JobName,
			"job_type": in.JobType,
		}
		// A job type whose input takes nothing must not be handed an empty
		// object: parameters is what its updater decodes, and some updaters are
		// selected by the job's type rather than by this field.
		if len(in.Parameters) > 0 {
			entry["parameters"] = in.Parameters
		}
		inputs = append(inputs, entry)
	}

	return map[string]any{
		"project_key":  req.ProjectName,
		"workflow_key": req.WorkflowName,
		"inputs":       inputs,
	}
}

// ExecuteCustomWorkflow POSTs the custom-workflow task to Zadig.
func (c *Client) ExecuteCustomWorkflow(ctx context.Context, req *ExecuteCustomWorkflowRequest) (*ExecuteWorkflowTask, error) {
	if req == nil {
		return nil, fmt.Errorf("zadig: nil request")
	}
	if strings.TrimSpace(req.ProjectName) == "" || strings.TrimSpace(req.WorkflowName) == "" {
		return nil, fmt.Errorf("zadig: project and workflow are required")
	}
	if len(req.Inputs) == 0 {
		// Zadig answers this with "no stage found in workflow task", but a task
		// that names no job is a caller error and is reported as one.
		return nil, fmt.Errorf("zadig: a custom workflow task needs at least one input")
	}

	raw, status, err := c.post(ctx, customTaskPath, CustomTaskBody(req))
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("zadig: execute workflow %q failed: status=%d body=%s",
			req.WorkflowName, status, baseclientset.Truncate(raw, 512))
	}

	task, err := decodeTask(raw)
	if err != nil {
		return nil, fmt.Errorf("zadig: execute workflow %q: %w", req.WorkflowName, err)
	}
	return task, nil
}

// ConsoleURL builds the web-console link to a workflow task.
func (c *Client) ConsoleURL(project, workflow string, taskID int64) string {
	if c.baseURL == "" || project == "" || workflow == "" {
		return ""
	}
	return fmt.Sprintf("%s/v1/projects/detail/%s/pipelines/%s/%s/%d",
		c.baseURL, url.PathEscape(project), consolePipelineKind, url.PathEscape(workflow), taskID)
}

// decodeTask reads the task out of a successful response.
//
// Zadig has returned the identifier both at the top level and nested under a
// "data" envelope across versions, and it can arrive as a JSON number or a
// string. All three are accepted rather than guessing one; an unreadable body
// is reported with the body text so the caller can see what Zadig actually said
// instead of being handed a silent zero.
func decodeTask(raw []byte) (*ExecuteWorkflowTask, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode response (body=%s): %w", baseclientset.Truncate(raw, 512), err)
	}

	scope := envelope
	if data, ok := envelope["data"].(map[string]any); ok {
		scope = data
	}

	task := &ExecuteWorkflowTask{Raw: envelope}
	for _, key := range []string{"task_id", "taskID"} {
		if id, ok := asInt64(scope[key]); ok {
			task.TaskID = id
			return task, nil
		}
	}
	return nil, fmt.Errorf("response carries no task_id (body=%s)", baseclientset.Truncate(raw, 512))
}

// asInt64 converts the numeric shapes JSON decoding produces into an int64.
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
	case string:
		var out int64
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &out); err == nil {
			return out, true
		}
	}
	return 0, false
}

// get sends a GET and returns the raw response plus its status code.
func (c *Client) get(ctx context.Context, path string, query url.Values) ([]byte, int, error) {
	resp, err := c.client.R(ctx).SetQueryParamsFromValues(query).Get(path)
	if err != nil {
		return nil, 0, fmt.Errorf("zadig: http request to %s: %w", path, err)
	}
	return c.response(ctx, resp, path)
}

// post sends a JSON body and returns the raw response plus its status code.
func (c *Client) post(ctx context.Context, path string, body any) ([]byte, int, error) {
	resp, err := c.client.R(ctx).SetBody(body).Post(path)
	if err != nil {
		return nil, 0, fmt.Errorf("zadig: http request to %s: %w", path, err)
	}
	return c.response(ctx, resp, path)
}

// response logs a completed request and returns its body and status. The token
// is deliberately absent from the log line; only the response side is ever
// logged.
func (c *Client) response(ctx context.Context, resp *resty.Response, path string) ([]byte, int, error) {
	if resp == nil {
		return nil, 0, fmt.Errorf("zadig: empty response from %s", path)
	}
	raw := resp.Bytes()
	slog.DebugContext(ctx, "zadig: response",
		"url", path,
		"status", resp.StatusCode(),
		"body_preview", baseclientset.Truncate(raw, 512),
	)
	return raw, resp.StatusCode(), nil
}
