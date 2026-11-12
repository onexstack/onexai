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

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/feizaonet/cobrax"

	"github.com/onexstack/onexai/internal/chat/pkg/clientset"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/zadig"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// stubZadig stands in for the Zadig client. It records the request it was
// handed, because most of what these tests are about is the mapping from flags
// to that request — an assertion on the reply alone would pass for a command
// that sent the wrong thing.
type stubZadig struct {
	zadig.Interface

	calls   int
	lastReq *zadig.ExecuteCustomWorkflowRequest
	err     error
	project string

	// detail is the workflow definition the command validates against; a nil one
	// answers with stubDetail().
	detail    *zadig.WorkflowDetail
	detailErr error

	// registries maps a registry id to the address the client resolves it to;
	// registryIDs records what was asked for, so a test can tell a resolution
	// apart from a hand-built string.
	registries  map[string]string
	registryErr error
	registryIDs []string
}

func (s *stubZadig) WorkflowDetail(_ context.Context, _, _ string) (*zadig.WorkflowDetail, error) {
	if s.detailErr != nil {
		return nil, s.detailErr
	}
	if s.detail != nil {
		return s.detail, nil
	}
	return stubDetail(), nil
}

func (s *stubZadig) RegistryAddress(_ context.Context, registryID string) (string, error) {
	s.registryIDs = append(s.registryIDs, registryID)
	if s.registryErr != nil {
		return "", s.registryErr
	}
	if addr, ok := s.registries[registryID]; ok {
		return addr, nil
	}
	return "ccr.example.com/ns", nil
}

func (s *stubZadig) ExecuteCustomWorkflow(_ context.Context, req *zadig.ExecuteCustomWorkflowRequest) (*zadig.ExecuteWorkflowTask, error) {
	s.calls++
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	return &zadig.ExecuteWorkflowTask{TaskID: 42, Raw: map[string]any{"task_id": 42}}, nil
}

func (s *stubZadig) ConsoleURL(project, workflow string, taskID int64) string {
	return "https://zadig.example.com/" + project + "/" + workflow + "/" + strconv.FormatInt(taskID, 10)
}

func (s *stubZadig) DefaultProject() string { return s.project }

func (s *stubZadig) Configured() bool { return true }

// newStub is a Zadig client that answers successfully and has a default project
// configured, which is the shape of a working deployment. Tests about the
// project's absence construct the struct directly instead.
func newStub() *stubZadig { return &stubZadig{project: "test-project"} }

// stubDetail is a workflow shaped like the one this command was written for: a
// build job over two services, and a deploy job that takes its services and
// images from that build job.
func stubDetail() *zadig.WorkflowDetail {
	return &zadig.WorkflowDetail{
		WorkflowKey: "release-x",
		Stages: []zadig.Stage{
			{Name: "构建", Jobs: []zadig.Job{buildJob("build", "reg-1", "gateway", "user")}},
			{Name: "部署", Jobs: []zadig.Job{deployJob("deploy", "prod", "build")}},
		},
	}
}

// standaloneDetail is a workflow whose deploy job takes its services from Zadig
// itself rather than from a build job (no source: fromjob). It is the fixture for
// what this command has to refuse: nothing in the request can say which services
// such a deploy should deploy.
func standaloneDetail() *zadig.WorkflowDetail {
	return &zadig.WorkflowDetail{
		WorkflowKey: "release-x",
		Stages: []zadig.Stage{
			{Name: "构建", Jobs: []zadig.Job{buildJob("build", "reg-1", "gateway", "user")}},
			{Name: "部署", Jobs: []zadig.Job{deployJob("deploy", "prod", "")}},
		},
	}
}

// buildJob renders a build job spec the way Zadig's detail endpoint returns it.
func buildJob(name, registryID string, services ...string) zadig.Job {
	list := make([]zadig.BuildService, 0, len(services))
	for _, svc := range services {
		list = append(list, zadig.BuildService{ServiceName: svc, ServiceModule: svc, BuildName: svc + "-build"})
	}
	return rawJob(name, zadig.JobTypeBuild, map[string]any{
		"docker_registry_id": registryID,
		"service_and_builds": list,
	})
}

// deployJob renders a deploy job spec. fromJob is the job it takes its services
// and images from, empty when it takes none.
func deployJob(name, env, fromJob string) zadig.Job {
	spec := map[string]any{"env": env}
	if fromJob != "" {
		spec["source"] = zadig.JobSourceFromJob
		spec["job_name"] = fromJob
	}
	return rawJob(name, zadig.JobTypeDeploy, spec)
}

func rawJob(name, jobType string, spec map[string]any) zadig.Job {
	raw, err := json.Marshal(spec)
	if err != nil {
		panic(err)
	}
	return zadig.Job{Name: name, Type: jobType, Spec: raw}
}

// stubClientset supplies the one client the command reaches for.
type stubClientset struct {
	clientset.Interface
	zadig zadig.Interface
}

func (s *stubClientset) Zadig() zadig.Interface { return s.zadig }

// runPipeline executes the real command with the given arguments and returns
// both the error and what the responder captured, so a test can assert that a
// failure was reported to the caller rather than only returned.
func runPipeline(t *testing.T, stub *stubZadig, args ...string) (error, *runtime.CommandResult) {
	t.Helper()

	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{Source: runtime.RequestSourceAPI})
	sr := &runtime.StaticRuntime{Clientset: &stubClientset{zadig: stub}}

	cmd := NewPipelineCmd(sr, out)
	cmd.SetArgs(args)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.ExecuteContext(context.Background())
	return err, out.Result()
}

// jobNames lists the jobs the request will run, in the order they were sent.
func jobNames(t *testing.T, req *zadig.ExecuteCustomWorkflowRequest) []string {
	t.Helper()
	if req == nil {
		t.Fatal("no request reached the client")
	}

	out := make([]string, 0, len(req.Inputs))
	for _, in := range req.Inputs {
		out = append(out, in.JobName)
	}
	return out
}

// buildServices lists the services a job input asks to build.
func buildServices(t *testing.T, in zadig.JobInput) []string {
	t.Helper()

	list, ok := in.Parameters["service_list"].([]map[string]any)
	if !ok {
		t.Fatalf("service_list is not a list of objects: %v", in.Parameters["service_list"])
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		out = append(out, entry["service_name"].(string))
	}
	return out
}

func TestPipelineTriggersWithTheSelection(t *testing.T) {
	stub := newStub()
	err, res := runPipeline(t, stub, "release-x", "-e", "prod", "-s", "gateway,user", "-t", "build", "-p", "my-project", "--confirm")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("client calls = %d, want 1", stub.calls)
	}

	req := stub.lastReq
	if req.ProjectName != "my-project" || req.WorkflowName != "release-x" {
		t.Errorf("request names = %+v", req)
	}
	if got := jobNames(t, req); !slices.Equal(got, []string{"build"}) {
		t.Errorf("jobs = %v, want just the build job", got)
	}

	build := req.Inputs[0]
	if build.JobType != zadig.JobTypeBuild {
		t.Errorf("job_type = %q, want %s", build.JobType, zadig.JobTypeBuild)
	}
	if got, want := buildServices(t, build), []string{"gateway", "user"}; !slices.Equal(got, want) {
		t.Errorf("services = %v, want %v", got, want)
	}
	// The registry is sent as the resolved "<address>/<namespace>" string, which
	// is the only form Zadig accepts for a build input.
	if got, want := build.Parameters["registry"], "ccr.example.com/ns"; got != want {
		t.Errorf("registry = %v, want %v", got, want)
	}
	if !slices.Equal(stub.registryIDs, []string{"reg-1"}) {
		t.Errorf("resolved registry ids = %v, want the build job's docker_registry_id", stub.registryIDs)
	}

	// All three representations have to be filled by the one SendResult: the
	// REST path keeps only Raw/Markdown/PlainText, and the MCP path falls back to
	// PlainText when it renders no card.
	if res.Raw == nil {
		t.Error("Raw is nil; the API surface would send an empty body")
	}
	if res.Markdown == nil || res.Markdown.Content == "" {
		t.Error("Markdown is empty; the Lark and API surfaces would send nothing")
	}
	if res.PlainText == "" {
		t.Error("PlainText is empty; the MCP surface would send nothing")
	}
	if _, err := json.Marshal(res.Raw); err != nil {
		t.Errorf("Raw is not JSON-marshalable: %v", err)
	}

	raw, _ := res.Raw.(map[string]any)
	if raw["task_id"] != int64(42) {
		t.Errorf("raw task_id = %v, want 42", raw["task_id"])
	}
	// The environment only reaches the wire through a deploy job's input, so the
	// reply is where a build-only run states the target it was given.
	if raw["target_env"] != "prod" {
		t.Errorf("raw target_env = %v, want prod", raw["target_env"])
	}
	if !strings.Contains(res.Markdown.Content, "https://zadig.example.com/my-project/release-x/42") {
		t.Errorf("markdown does not carry the console link:\n%s", res.Markdown.Content)
	}
	if !strings.Contains(res.Markdown.Content, "gateway") {
		t.Errorf("markdown does not name the services being built:\n%s", res.Markdown.Content)
	}
	if !strings.Contains(res.PlainText, "42") {
		t.Errorf("plain text does not carry the task id: %s", res.PlainText)
	}
	// The jobs are the task: the reply has to state them, not leave the caller to
	// infer them from the flags.
	jobs, ok := raw["jobs"].([]map[string]any)
	if !ok || len(jobs) != 1 {
		t.Fatalf("raw jobs = %v, want the one job the task consists of", raw["jobs"])
	}
	if jobs[0]["job_name"] != "build" {
		t.Errorf("raw jobs[0] = %v, want the build job", jobs[0])
	}
}

// TestPipelineWithoutConfirmIsAPreview pins the safety property: no --confirm
// means no call, but the caller still learns what would have been sent.
func TestPipelineWithoutConfirmIsAPreview(t *testing.T) {
	stub := newStub()
	err, res := runPipeline(t, stub, "release-x", "-e", "prod")
	if err == nil {
		t.Fatal("Execute() error = nil, want a rejection")
	}
	if !runtime.IsUserError(err) {
		t.Errorf("error is not a UserError: %v", err)
	}
	if stub.calls != 0 {
		t.Fatalf("client calls = %d, want 0", stub.calls)
	}

	if !strings.Contains(err.Error(), "--confirm") {
		t.Errorf("error does not say how to confirm: %v", err)
	}
	if res.Markdown == nil || !strings.Contains(res.Markdown.Content, "--confirm") {
		t.Error("the preview does not reach the markdown surface")
	}
	if res.PlainText == "" {
		t.Error("the preview does not reach the plain-text surface")
	}
	raw, _ := res.Raw.(map[string]any)
	if raw["triggered"] != false {
		t.Errorf("raw triggered = %v, want false", raw["triggered"])
	}
	if raw["request_body"] == nil {
		t.Error("the preview does not echo the request body")
	}

	// The preview is the answer to "does the default run include the deploy job?",
	// so it has to echo the whole selection the command resolved, not just the
	// flags.
	body, ok := raw["request_body"].(map[string]any)
	if !ok {
		t.Fatalf("request_body is not an object: %v", raw["request_body"])
	}
	inputs, ok := body["inputs"].([]map[string]any)
	if !ok || len(inputs) != 2 {
		t.Fatalf("preview inputs = %v, want the build and deploy jobs", body["inputs"])
	}
	if inputs[1]["job_name"] != "deploy" {
		t.Errorf("preview inputs[1] = %v, want the deploy job", inputs[1])
	}

	// What the deploy job will deploy is not visible in the flags, so the preview
	// has to carry it: Zadig rejects a deploy input that names no service, and the
	// service list is the part of the request a caller can be surprised by.
	deploy, ok := inputs[1]["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("preview deploy parameters = %v", inputs[1]["parameters"])
	}
	if got, want := deploy["env_name"], "prod"; got != want {
		t.Errorf("preview deploy env_name = %v, want %v", got, want)
	}
	list, ok := deploy["service_list"].([]map[string]any)
	if !ok || len(list) != 2 {
		t.Fatalf("preview deploy service_list = %v, want the build job's two services", deploy["service_list"])
	}

	jobs, ok := raw["jobs"].([]map[string]any)
	if !ok || len(jobs) != 2 {
		t.Fatalf("preview jobs = %v, want both jobs", raw["jobs"])
	}
	got, ok := jobs[1]["services"].([]string)
	if !ok || !slices.Equal(got, []string{"gateway", "user"}) {
		t.Errorf("preview deploy services = %v, want the build job's", jobs[1]["services"])
	}
}

// TestPipelineSelectsJobsByPresence is the regression test for the bug this
// command was rewritten around: a custom workflow task is composed from the jobs
// its inputs name, so "build only" has to leave the deploy job out of the
// request. Anything that merely marked it and relied on Zadig to skip it would
// deploy when only a build was intended.
func TestPipelineSelectsJobsByPresence(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		detail   *zadig.WorkflowDetail
		wantJobs []string
	}{
		{name: "no flag runs every job", wantJobs: []string{"build", "deploy"}},
		{name: "--ci runs build only", args: []string{"--ci"}, wantJobs: []string{"build"}},
		{name: "--stage build", args: []string{"-t", "build"}, wantJobs: []string{"build"}},
		{name: "--stage 构建", args: []string{"-t", "构建"}, wantJobs: []string{"build"}},
		{name: "stage is case-insensitive", args: []string{"-t", "BUILD"}, wantJobs: []string{"build"}},
		{name: "a trailing comma is ignored", args: []string{"-t", "build,"}, wantJobs: []string{"build"}},
		{name: "--stage with both", args: []string{"-t", "build,deploy"}, wantJobs: []string{"build", "deploy"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newStub()
			stub.detail = tt.detail
			args := append([]string{"release-x", "-e", "prod", "--confirm"}, tt.args...)
			if err, _ := runPipeline(t, stub, args...); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			if got := jobNames(t, stub.lastReq); !slices.Equal(got, tt.wantJobs) {
				t.Errorf("jobs = %v, want %v", got, tt.wantJobs)
			}

			var built []string
			for _, in := range stub.lastReq.Inputs {
				if in.JobType == zadig.JobTypeBuild {
					built = buildServices(t, in)
				}
			}

			// A deploy job fed by a build job has to be told which services to
			// deploy: Zadig replaces the job's own service list with the input's, so
			// an input that names none is refused ("deploy service is empty for env
			// ..."). The selection is the build job's, because that is where Zadig
			// reads each service's image from.
			for _, in := range stub.lastReq.Inputs {
				if in.JobType != zadig.JobTypeDeploy {
					continue
				}
				if got, want := in.Parameters["env_name"], "prod"; got != want {
					t.Errorf("deploy env_name = %v, want %v", got, want)
				}
				if got := buildServices(t, in); !slices.Equal(got, built) {
					t.Errorf("deploy services = %v, want the build job's %v", got, built)
				}
			}
		})
	}
}

// TestPipelineRefusesADeployWithoutItsBuildJob pins the one selection that
// cannot work: this request carries no image references of its own, so a deploy
// job fed by a build job (source: fromjob) has nothing to deploy if that build
// job is left out — and Zadig would run it anyway.
func TestPipelineRefusesADeployWithoutItsBuildJob(t *testing.T) {
	stub := newStub()
	err, _ := runPipeline(t, stub, "release-x", "-e", "prod", "-t", "deploy", "--confirm")
	if err == nil {
		t.Fatal("Execute() error = nil, want the selection refused")
	}
	if !runtime.IsUserError(err) {
		t.Errorf("error is not a UserError: %v", err)
	}
	if !strings.Contains(err.Error(), "build") {
		t.Errorf("error does not name the build job the deploy needs: %v", err)
	}
	if stub.calls != 0 {
		t.Errorf("client calls = %d, want 0 — a refused selection must not trigger", stub.calls)
	}
}

// TestPipelineRefusesADeployNotFedByABuild covers the deploy job this command
// cannot trigger at all: Zadig's deploy input has to name the services to deploy
// (an input that names none is answered with "deploy service is empty for env
// ..."), and only a job fed by a build job lets this request work that list out.
// A job that takes its services from Zadig's own configuration is refused rather
// than sent an empty list.
func TestPipelineRefusesADeployNotFedByABuild(t *testing.T) {
	for _, args := range [][]string{
		{"-t", "deploy"},
		{"-t", "部署"},
		{}, // the whole workflow, deploy included
	} {
		stub := newStub()
		stub.detail = standaloneDetail()
		err, _ := runPipeline(t, stub, append([]string{"release-x", "-e", "prod", "--confirm"}, args...)...)
		if err == nil {
			t.Fatalf("%v: Execute() error = nil, want the deploy job refused", args)
		}
		if !runtime.IsUserError(err) {
			t.Errorf("%v: error is not a UserError: %v", args, err)
		}
		if !strings.Contains(err.Error(), "fromjob") {
			t.Errorf("%v: error does not say what the deploy job needs: %v", args, err)
		}
		if stub.calls != 0 {
			t.Errorf("%v: client calls = %d, want 0 — a refused selection must not trigger", args, stub.calls)
		}
	}
}

// TestPipelineDeploysOnlyTheSelectedServices pins that -s narrows a deploy job's
// input the way it narrows the build job's, module included: Zadig matches an
// input service on the name/module pair, and a deployment cannot name a service
// the build job is not building — its image would have no producer.
func TestPipelineDeploysOnlyTheSelectedServices(t *testing.T) {
	stub := newStub()
	if err, _ := runPipeline(t, stub, "release-x", "-e", "prod", "-s", "gateway", "--confirm"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	for _, in := range stub.lastReq.Inputs {
		if got, want := buildServices(t, in), []string{"gateway"}; !slices.Equal(got, want) {
			t.Errorf("%s services = %v, want %v", in.JobType, got, want)
		}
		list, ok := in.Parameters["service_list"].([]map[string]any)
		if !ok || len(list) != 1 {
			t.Fatalf("%s service_list = %v, want one entry", in.JobType, in.Parameters["service_list"])
		}
		if got, want := list[0]["service_module"], "gateway"; got != want {
			t.Errorf("%s service_module = %v, want %v — Zadig matches on the pair", in.JobType, got, want)
		}
	}
}

// TestPipelineRefusesADeployWhoseBuildJobHasNothingToBuild covers the hole -s can
// open across two build jobs: one build job builds nothing this run and is left
// out of the task, so the deploy job fed by it would be handed an empty service
// list. Refusing here says why, one step before Zadig would.
func TestPipelineRefusesADeployWhoseBuildJobHasNothingToBuild(t *testing.T) {
	detail := func() *zadig.WorkflowDetail {
		return &zadig.WorkflowDetail{
			Stages: []zadig.Stage{
				{Name: "构建", Jobs: []zadig.Job{
					buildJob("build-web", "reg-1", "gateway"),
					buildJob("build-svc", "reg-1", "user"),
				}},
				{Name: "部署", Jobs: []zadig.Job{deployJob("deploy", "prod", "build-svc")}},
			},
		}
	}

	stub := newStub()
	stub.detail = detail()
	err, _ := runPipeline(t, stub, "release-x", "-e", "prod", "-s", "gateway", "--confirm")
	if err == nil {
		t.Fatal("Execute() error = nil, want the deploy refused")
	}
	if !strings.Contains(err.Error(), "build-svc") {
		t.Errorf("error does not name the build job the deploy takes its services from: %v", err)
	}
	if stub.calls != 0 {
		t.Errorf("client calls = %d, want 0", stub.calls)
	}

	// The same workflow with a -s the quoted build job can serve runs both jobs.
	stub = newStub()
	stub.detail = detail()
	if err, _ := runPipeline(t, stub, "release-x", "-e", "prod", "-s", "user", "--confirm"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := jobNames(t, stub.lastReq), []string{"build-svc", "deploy"}; !slices.Equal(got, want) {
		t.Errorf("jobs = %v, want %v", got, want)
	}
}

// TestPipelineRefusesADeployEnvironmentMismatch covers what the workflow-wide
// environment check cannot: a workflow that deploys to two environments, where
// -e names one of them and a selected deploy job wants the other. Both deploy
// jobs stand alone, so the only thing wrong with the selection is the
// environment.
func TestPipelineRefusesADeployEnvironmentMismatch(t *testing.T) {
	stub := newStub()
	stub.detail = &zadig.WorkflowDetail{
		Stages: []zadig.Stage{
			{Name: "构建", Jobs: []zadig.Job{buildJob("build", "reg-1", "gateway")}},
			{Name: "部署", Jobs: []zadig.Job{
				deployJob("deploy-prod", "prod", ""),
				deployJob("deploy-staging", "staging", ""),
			}},
		},
	}

	err, _ := runPipeline(t, stub, "release-x", "-e", "prod", "-t", "deploy", "--confirm")
	if err == nil {
		t.Fatal("Execute() error = nil, want the mismatch refused")
	}
	if !strings.Contains(err.Error(), "staging") {
		t.Errorf("error does not name the job's environment: %v", err)
	}
	if stub.calls != 0 {
		t.Errorf("client calls = %d, want 0", stub.calls)
	}
}

func TestPipelineRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
		// noConfiguredProject drops the default project, so the case is about a
		// deployment that configured none.
		noConfiguredProject bool
	}{
		{name: "no pipeline name", args: []string{"--confirm"}, want: "accepts 1 arg"},
		{name: "too many positionals", args: []string{"a", "b", "--confirm"}, want: "accepts 1 arg"},
		{name: "unknown stage", args: []string{"release-x", "-e", "prod", "-t", "k8s", "--confirm"}, want: "build"},
		{
			// Refused rather than resolved: --ci is "build only", so pairing it
			// with a stage list is the caller contradicting itself, and letting
			// one win could deploy when only a build was asked for.
			name: "ci and stage are mutually exclusive",
			args: []string{"release-x", "-e", "prod", "--ci", "-t", "deploy", "--confirm"},
			want: "不能同时指定",
		},
		{
			// --env is declared required (MarkFlagRequired), so cobra refuses
			// before RunE and the message is cobra's own `required flag(s) "env"
			// not set` rather than Validate's localized line below. That is the
			// deliberate price of the declaration: the requirement has to reach
			// the MCP tool schema, where a model reads it before calling, and
			// cobrax takes it from the same annotation cobra validates. Validate
			// still produces the friendly message for the whitespace case and for
			// callers that never pass through cobra's flag validation.
			name: "missing env",
			args: []string{"release-x", "-p", "p", "--confirm"},
			want: "env",
		},
		{
			name:                "missing project",
			args:                []string{"release-x", "-e", "prod", "--confirm"},
			want:                "zadig-project",
			noConfiguredProject: true,
		},
		{
			// A service name Zadig does not know would be dropped from the build
			// job's service list, and a build of nothing looks like a build that
			// succeeded.
			name: "a service the workflow does not have",
			args: []string{"release-x", "-e", "prod", "-s", "gateway,typo", "--confirm"},
			want: "typo",
		},
		{
			name: "a perfectly valid service on a different workflow",
			args: []string{"release-x", "-e", "prod", "-s", "gateway", "-t", "deploy", "--confirm"},
			want: "--service",
		},
		{
			// The environment is the one thing a release must state, so a value
			// the workflow does not deploy to is refused rather than passed on.
			name: "an environment the workflow does not deploy to",
			args: []string{"release-x", "-e", "staging", "--confirm"},
			want: "prod",
		},
		{
			// And it is checked the same way for a run that does not deploy:
			// otherwise the same -e would be accepted here and refused by the run
			// that does.
			name: "an environment the workflow does not deploy to, on a build-only run",
			args: []string{"release-x", "-e", "staging", "--ci", "--confirm"},
			want: "prod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newStub()
			if tt.noConfiguredProject {
				stub.project = ""
			}

			err, _ := runPipeline(t, stub, tt.args...)
			if err == nil {
				t.Fatalf("Execute() error = nil, want one containing %q", tt.want)
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.want)
			}
			if stub.calls != 0 {
				t.Errorf("client calls = %d, want 0 — a rejected command must not trigger", stub.calls)
			}
		})
	}
}

// TestPipelineSurfacesWorkflowReadFailure checks a failure that happens before
// the trigger is reported the same way as a failed trigger: as a user-facing
// message on every surface, with no call to the task endpoint.
func TestPipelineSurfacesWorkflowReadFailure(t *testing.T) {
	stub := newStub()
	stub.detailErr = errors.New(`read workflow "release-x" failed: status=404 body={"message":"Invalid path"}`)

	err, res := runPipeline(t, stub, "release-x", "-e", "prod", "--confirm")
	if err == nil {
		t.Fatal("Execute() error = nil, want one")
	}
	if !runtime.IsUserError(err) {
		// A workflow the operator cannot read is a naming or permissions mistake
		// on their side, unlike a rejection from Zadig itself.
		t.Errorf("error is not a UserError: %v", err)
	}
	if stub.calls != 0 {
		t.Errorf("client calls = %d, want 0", stub.calls)
	}
	if res.Markdown == nil || !strings.Contains(res.Markdown.Content, "Invalid path") {
		t.Errorf("the failure is not in the reply:\n%v", res.Markdown)
	}
	if res.PlainText == "" {
		t.Error("PlainText is empty; the API surface would send an empty body")
	}
}

func TestPipelineFallsBackToTheConfiguredProject(t *testing.T) {
	stub := &stubZadig{project: "configured-project"}
	if err, _ := runPipeline(t, stub, "release-x", "-e", "prod", "--confirm"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stub.lastReq.ProjectName != "configured-project" {
		t.Errorf("project = %q, want the configured default", stub.lastReq.ProjectName)
	}

	// And the flag beats the default.
	stub = &stubZadig{project: "configured-project"}
	if err, _ := runPipeline(t, stub, "release-x", "-e", "prod", "-p", "override", "--confirm"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stub.lastReq.ProjectName != "override" {
		t.Errorf("project = %q, want the flag value", stub.lastReq.ProjectName)
	}
}

// TestPipelineSurfacesZadigFailure is the regression test for the REST/MCP
// empty-body failure: the returned error alone is dropped by the API caller, so
// the failure has to be written into the responder as well.
func TestPipelineSurfacesZadigFailure(t *testing.T) {
	stub := newStub()
	stub.err = errors.New("zadig: execute workflow failed: status=400 body=服务不存在")
	err, res := runPipeline(t, stub, "release-x", "-e", "prod", "--confirm")
	if err == nil {
		t.Fatal("Execute() error = nil, want one")
	}
	if runtime.IsUserError(err) {
		// A Zadig-side rejection is not the caller's mistake, and reporting it
		// as one would suppress the error metric that would show the trend.
		t.Errorf("error is a UserError, want a server-side error: %v", err)
	}

	if res.Markdown == nil || !strings.Contains(res.Markdown.Content, "服务不存在") {
		t.Errorf("Zadig's message is not in the reply:\n%v", res.Markdown)
	}
	if res.PlainText == "" {
		t.Error("PlainText is empty; the API surface would send an empty body")
	}
	raw, _ := res.Raw.(map[string]any)
	if raw["triggered"] != false {
		t.Errorf("raw triggered = %v, want false", raw["triggered"])
	}
	if raw["request_body"] == nil {
		// The caller has to be able to see what Zadig rejected, not just that it
		// was rejected.
		t.Error("the failure does not echo the request body")
	}
	if _, err := json.Marshal(res.Raw); err != nil {
		t.Errorf("Raw is not JSON-marshalable: %v", err)
	}
}

func TestPipelineServiceNormalization(t *testing.T) {
	stub := newStub()
	if err, _ := runPipeline(t, stub, "release-x", "-e", "prod", "-s", " gateway ,,user, gateway ", "--confirm"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got, want := buildServices(t, stub.lastReq.Inputs[0]), []string{"gateway", "user"}; !slices.Equal(got, want) {
		t.Errorf("services = %v, want %v", got, want)
	}
}

// TestPipelineResolvesEachRegistryOnce checks the build inputs carry a resolvable
// registry string: Zadig rejects a registry id, so every job's
// docker_registry_id has to go through the registry list, and two jobs sharing
// one registry need only ask once.
func TestPipelineResolvesEachRegistryOnce(t *testing.T) {
	stub := newStub()
	stub.registries = map[string]string{"reg-1": "ccr.example.com/feizaonet"}
	stub.detail = &zadig.WorkflowDetail{
		Stages: []zadig.Stage{
			{Name: "构建", Jobs: []zadig.Job{
				buildJob("build-a", "reg-1", "gateway"),
				buildJob("build-b", "reg-1", "user"),
			}},
			{Name: "部署", Jobs: []zadig.Job{deployJob("deploy", "prod", "build-a")}},
		},
	}

	if err, _ := runPipeline(t, stub, "release-x", "-e", "prod", "-p", "p", "--confirm"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !slices.Equal(stub.registryIDs, []string{"reg-1"}) {
		t.Errorf("registry lookups = %v, want one lookup for the shared registry", stub.registryIDs)
	}
	for _, in := range stub.lastReq.Inputs {
		if in.JobType != zadig.JobTypeBuild {
			continue
		}
		if got, want := in.Parameters["registry"], "ccr.example.com/feizaonet"; got != want {
			t.Errorf("job %s registry = %v, want %v", in.JobName, got, want)
		}
	}
}

// TestPipelineRefusesABuildWithoutARegistry pins the refusal instead of a task
// that would be rejected by Zadig with a message about an image registry the
// operator never mentioned.
func TestPipelineRefusesABuildWithoutARegistry(t *testing.T) {
	stub := newStub()
	stub.detail = &zadig.WorkflowDetail{
		Stages: []zadig.Stage{{Name: "构建", Jobs: []zadig.Job{buildJob("build", "", "gateway")}}},
	}

	err, _ := runPipeline(t, stub, "release-x", "-e", "prod", "-p", "p", "--confirm")
	if err == nil {
		t.Fatal("Execute() error = nil, want one")
	}
	if !runtime.IsUserError(err) {
		t.Errorf("error is not a UserError: %v", err)
	}
	if !strings.Contains(err.Error(), "build") {
		t.Errorf("error does not name the job without a registry: %v", err)
	}
	if stub.calls != 0 {
		t.Errorf("client calls = %d, want 0", stub.calls)
	}
}

func TestPipelineDeclaration(t *testing.T) {
	cmd := NewPipelineCmd(&runtime.StaticRuntime{}, runtime.NewStructuredResponder(&runtime.DynamicRuntime{}))

	if cmd.Use != "pipeline <pipeline_name>" {
		t.Errorf("Use = %q, want the positional declared in it", cmd.Use)
	}
	if !slices.Contains(cmd.Aliases, "cicd") {
		t.Errorf("aliases = %v, want cicd", cmd.Aliases)
	}

	want := map[string]string{
		cobrax.AnnotationArgPrefix + "0": "",
		cobrax.AnnotationTitle:           "触发 Zadig 自定义工作流",
		cobrax.AnnotationReadOnly:        "false",
		cobrax.AnnotationDestructive:     "true",
		cobrax.AnnotationIdempotent:      "false",
		cobrax.AnnotationOpenWorld:       "true",
		chat.MCPTool:                     chat.True,
		chat.SREOnly:                     chat.True,
		chat.ChatGroup:                   chat.ChatGroupPlatform,
	}
	for key, value := range want {
		got, ok := cmd.Annotations[key]
		if !ok {
			t.Errorf("annotation %q is missing", key)
			continue
		}
		if value != "" && got != value {
			t.Errorf("annotation %q = %q, want %q", key, got, value)
		}
	}

	if cmd.Args == nil {
		t.Error("Args is nil; the pipeline name would not be required")
	}

	// The MCP bridge turns the tree into a tool schema and cannot render a
	// hidden command, so this must never be hidden.
	if cmd.Hidden {
		t.Error("the command is hidden from the MCP bridge")
	}
}

// TestResolvePhasesAndNormalizeServices cover the pure helpers directly: they
// hold the two rules an operator would notice if they broke, and a table here
// documents them better than the flag-driven cases above.
func TestResolvePhases(t *testing.T) {
	tests := []struct {
		name       string
		ci         bool
		stages     []string
		wantBuild  bool
		wantDeploy bool
		wantErr    bool
	}{
		{name: "unset runs both", wantBuild: true, wantDeploy: true},
		{name: "ci is build only", ci: true, wantBuild: true},
		{name: "ci with stages is refused", ci: true, stages: []string{"build"}, wantErr: true},
		{name: "unknown stage", stages: []string{"stage"}, wantErr: true},
		{name: "only empty values", stages: []string{""}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			build, deploy, err := resolvePhases(tt.ci, tt.stages)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolvePhases() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if build != tt.wantBuild || deploy != tt.wantDeploy {
				t.Errorf("resolvePhases() = build:%v deploy:%v, want build:%v deploy:%v", build, deploy, tt.wantBuild, tt.wantDeploy)
			}
		})
	}
}

func TestNormalizeServices(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "empty", in: nil, want: nil},
		{name: "trims and drops empties", in: []string{" a ", "", " b "}, want: []string{"a", "b"}},
		{name: "de-duplicates in order", in: []string{"b", "a", "b"}, want: []string{"b", "a"}},
		{name: "splits commas inside an element", in: []string{"a,b"}, want: []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeServices(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("normalizeServices(%v) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("normalizeServices(%v) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}

// TestPipelineFailsLoudlyWithoutAStaticRuntime pins the error instead of a
// panic: the command is reachable from a tree built in a test or a misconfigured
// server, and a nil dereference there takes the whole request down.
func TestPipelineFailsLoudlyWithoutAStaticRuntime(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{Source: runtime.RequestSourceAPI})
	cmd := NewPipelineCmd(nil, out)
	cmd.SetArgs([]string{"release-x", "-e", "prod", "--confirm"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("Execute() error = nil, want one")
	}
}

// TestPipelineLarkFailureDoesNotDoubleReply guards the branch in fail(): the
// Lark path renders the returned error itself, so writing a result as well would
// post the failure twice.
func TestPipelineLarkFailureDoesNotDoubleReply(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{Source: runtime.RequestSourceLark})
	stub := &stubZadig{}
	sr := &runtime.StaticRuntime{Clientset: &stubClientset{zadig: stub}}

	cmd := NewPipelineCmd(sr, out)
	cmd.SetArgs([]string{"release-x", "-e", "prod", "-p", "my-project"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	if err := cmd.ExecuteContext(runtime.Inject(context.Background(), &runtime.DynamicRuntime{Source: runtime.RequestSourceLark})); err == nil {
		t.Fatal("Execute() error = nil, want the rejection")
	}
	if res := out.Result(); res.Markdown != nil {
		t.Errorf("a Lark failure also wrote a card, which would reply twice:\n%v", res.Markdown)
	}
}
