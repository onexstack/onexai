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

package zadig

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// route is one canned answer for a path.
type route struct {
	status int
	body   string
}

// recordedRequest is one request the test server saw: the wire, not the client's
// own structs.
type recordedRequest struct {
	method string
	path   string
	// escaped is the path as it went on the wire, before the server decoded it:
	// the difference is what tells an escaped name apart from a name that broke
	// out of its segment.
	escaped string
	query   url.Values
	auth    string
	ctype   string
	body    map[string]any
}

// recorder keeps the requests a test server received.
type recorder struct {
	requests []recordedRequest
}

func (r *recorder) add(req recordedRequest) { r.requests = append(r.requests, req) }

// count reports how many requests reached the server.
func (r *recorder) count() int { return len(r.requests) }

// to returns the first request that reached path.
func (r *recorder) to(path string) (recordedRequest, bool) {
	for _, req := range r.requests {
		if req.path == path {
			return req, true
		}
	}
	return recordedRequest{}, false
}

// newRecorder starts a server that answers each path from answers, recording
// every request it receives. A path the test did not answer gets Zadig's own
// "Invalid path" 404 rather than a Go 404, so a missing route surfaces as the
// error the live instance would give.
func newRecorder(t *testing.T, answers map[string]route) (*httptest.Server, *recorder) {
	t.Helper()

	rec := &recorder{}
	mux := http.NewServeMux()
	for path, answer := range answers {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			req := recordedRequest{
				method:  r.Method,
				path:    r.URL.Path,
				escaped: r.URL.EscapedPath(),
				query:   r.URL.Query(),
				auth:    r.Header.Get("Authorization"),
				ctype:   r.Header.Get("Content-Type"),
			}
			_ = json.Unmarshal(raw, &req.body)
			rec.add(req)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(answer.status)
			_, _ = w.Write([]byte(answer.body))
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rec.add(recordedRequest{method: r.Method, path: r.URL.Path})
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Invalid path"}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, rec
}

// newTaskServer is newRecorder for the common case of a test that only triggers
// a task.
func newTaskServer(t *testing.T, status int, response string) (*httptest.Server, *recorder) {
	t.Helper()
	return newRecorder(t, map[string]route{
		customTaskPath: {status: status, body: response},
	})
}

func TestExecuteCustomWorkflowSendsExpectedRequest(t *testing.T) {
	srv, rec := newTaskServer(t, http.StatusOK, `{"task_id": 42}`)

	client := NewClient(srv.URL, "test-token", "my-project")
	task, err := client.ExecuteCustomWorkflow(context.Background(), &ExecuteCustomWorkflowRequest{
		ProjectName:  "my-project",
		WorkflowName: "release-x",
		Inputs: []JobInput{
			{
				JobName: "build", JobType: JobTypeBuild,
				Parameters: map[string]any{
					"registry": "ccr.example.com/ns",
					"service_list": []map[string]any{
						{"service_name": "gateway", "service_module": "gateway"},
						{"service_name": "user", "service_module": "user"},
					},
				},
			},
			{JobName: "deploy", JobType: JobTypeDeploy, Parameters: map[string]any{"env_name": "prod"}},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteCustomWorkflow() error = %v", err)
	}
	if task.TaskID != 42 {
		t.Errorf("TaskID = %d, want 42", task.TaskID)
	}
	if task.Raw == nil {
		t.Error("Raw is nil, want the decoded response kept for callers")
	}

	got, ok := rec.to(customTaskPath)
	if !ok {
		t.Fatalf("no request reached %s", customTaskPath)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.method)
	}
	if got.auth == "" || !strings.HasPrefix(got.auth, "Bearer ") {
		// The token itself is a secret, so only its presence and scheme are
		// asserted; a literal here would put it in the test output.
		t.Errorf("Authorization = %q, want a Bearer header", got.auth)
	}
	if got.ctype != "application/json" {
		t.Errorf("Content-Type = %s, want application/json", got.ctype)
	}

	if got.body["project_key"] != "my-project" || got.body["workflow_key"] != "release-x" {
		t.Errorf("body keys = %v, want my-project/release-x", got.body)
	}

	inputs, ok := got.body["inputs"].([]any)
	if !ok {
		t.Fatalf("inputs missing or not an array: %v", got.body["inputs"])
	}
	if len(inputs) != 2 {
		t.Fatalf("inputs = %v, want the two jobs", inputs)
	}

	build, ok := inputs[0].(map[string]any)
	if !ok {
		t.Fatalf("inputs[0] is not an object: %v", inputs[0])
	}
	if build["job_name"] != "build" || build["job_type"] != JobTypeBuild {
		t.Errorf("inputs[0] = %v, want the build job named and typed", build)
	}
	params, ok := build["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("inputs[0].parameters is not an object: %v", build["parameters"])
	}
	if params["registry"] != "ccr.example.com/ns" {
		// An id or an empty value here is rejected by Zadig, so the resolved
		// address is the only useful thing to assert.
		t.Errorf("parameters.registry = %v, want the address/namespace form", params["registry"])
	}
	assertServiceList(t, params["service_list"], []string{"gateway", "user"})

	deploy, ok := inputs[1].(map[string]any)
	if !ok {
		t.Fatalf("inputs[1] is not an object: %v", inputs[1])
	}
	deployParams, ok := deploy["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("inputs[1].parameters is not an object: %v", deploy["parameters"])
	}
	if deployParams["env_name"] != "prod" {
		t.Errorf("parameters.env_name = %v, want prod", deployParams["env_name"])
	}
}

// TestCustomTaskBodyInputsEncodeByPresence pins how a selection reaches the wire:
// the task is composed from the jobs the inputs name, so "build only" has to mean
// the deploy job is absent — there is no skip flag to set.
func TestCustomTaskBodyInputsEncodeByPresence(t *testing.T) {
	tests := []struct {
		name          string
		inputs        []JobInput
		wantJobs      []string
		wantParamKeys []string
	}{
		{
			name:          "build only leaves the deploy job out entirely",
			inputs:        []JobInput{{JobName: "build", JobType: JobTypeBuild, Parameters: map[string]any{"registry": "r"}}},
			wantJobs:      []string{"build"},
			wantParamKeys: []string{"registry"},
		},
		{
			name:          "a full run names both jobs",
			inputs:        []JobInput{{JobName: "build", JobType: JobTypeBuild}, {JobName: "deploy", JobType: JobTypeDeploy}},
			wantJobs:      []string{"build", "deploy"},
			wantParamKeys: []string{"", ""},
		},
		{
			name: "a job type that takes no parameters omits the key",
			inputs: []JobInput{
				{JobName: "check", JobType: "zadig-testing"},
			},
			wantJobs:      []string{"check"},
			wantParamKeys: []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := CustomTaskBody(&ExecuteCustomWorkflowRequest{
				ProjectName: "p", WorkflowName: "w", Inputs: tt.inputs,
			})

			inputs, ok := body["inputs"].([]map[string]any)
			if !ok {
				t.Fatalf("inputs is not an array of objects: %v", body["inputs"])
			}
			if len(inputs) != len(tt.wantJobs) {
				t.Fatalf("inputs = %v, want %d entries", inputs, len(tt.wantJobs))
			}
			for i, want := range tt.wantJobs {
				if inputs[i]["job_name"] != want {
					t.Errorf("inputs[%d].job_name = %v, want %s", i, inputs[i]["job_name"], want)
				}
				_, hasParams := inputs[i]["parameters"]
				if hasParams != (tt.wantParamKeys[i] != "") {
					t.Errorf("inputs[%d].parameters present = %v, want %v", i, hasParams, tt.wantParamKeys[i] != "")
				}
			}
		})
	}

	if got := CustomTaskBody(nil); got != nil {
		t.Errorf("CustomTaskBody(nil) = %v, want nil", got)
	}
}

func assertServiceList(t *testing.T, got any, want []string) {
	t.Helper()

	list, ok := got.([]any)
	if !ok {
		t.Fatalf("service_list is not an array: %v", got)
	}
	if len(list) != len(want) {
		t.Fatalf("service_list = %v, want %v", list, want)
	}
	for i, entry := range list {
		obj, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("service_list[%d] is not an object: %v", i, entry)
		}
		if obj["service_name"] != want[i] {
			t.Errorf("service_list[%d].service_name = %v, want %v", i, obj["service_name"], want[i])
		}
		if obj["service_module"] != want[i] {
			// Zadig matches an input service on name AND module, so a dropped
			// module would make the service unmatchable and the build empty.
			t.Errorf("service_list[%d].service_module = %v, want %v", i, obj["service_module"], want[i])
		}
	}
}

// TestWorkflowDetailReadsJobsAndSpecs checks the definition is read as the command
// needs it: every job in stage order, with the per-type spec values the request
// and the validation are built from.
func TestWorkflowDetailReadsJobsAndSpecs(t *testing.T) {
	const path = "/openapi/workflows/custom/release-x/detail"
	body := `{
		"project_key": "p",
		"workflow_key": "release-x",
		"stages": [
			{"name": "构建", "jobs": [
				{"name": "build", "type": "zadig-build", "spec": {
					"docker_registry_id": "reg-1",
					"service_and_builds": [
						{"service_name": "gateway", "service_module": "gateway", "build_name": "gateway-build"},
						{"service_name": "user", "service_module": "user", "build_name": "user-build"}
					]}},
				{"name": "lint", "type": "zadig-testing", "spec": {"script": "make lint"}}
			]},
			{"name": "部署", "jobs": [
				{"name": "deploy", "type": "zadig-deploy", "spec": {"env": "prod", "source": "fromjob", "job_name": "build"}},
				{"name": "deploy-manual", "type": "zadig-deploy", "spec": {"env": "prod"}}
			]}
		]
	}`
	srv, rec := newRecorder(t, map[string]route{path: {status: http.StatusOK, body: body}})
	client := NewClient(srv.URL, "token", "p")

	detail, err := client.WorkflowDetail(context.Background(), "p", "release-x")
	if err != nil {
		t.Fatalf("WorkflowDetail() error = %v", err)
	}

	got, ok := rec.to(path)
	if !ok {
		t.Fatalf("no request reached %s", path)
	}
	if got.method != http.MethodGet {
		t.Errorf("method = %s, want GET", got.method)
	}
	if got.query.Get("projectKey") != "p" {
		// The route is flat, so a missing projectKey would read another project's
		// workflow rather than fail.
		t.Errorf("projectKey = %q, want p", got.query.Get("projectKey"))
	}

	jobs := detail.Jobs()
	want := []string{"build", "lint", "deploy", "deploy-manual"}
	if len(jobs) != len(want) {
		t.Fatalf("Jobs() = %d jobs, want %d", len(jobs), len(want))
	}
	for i, name := range want {
		if jobs[i].Name != name {
			t.Errorf("Jobs()[%d].Name = %s, want %s (stage order)", i, jobs[i].Name, name)
		}
	}

	build := jobs[0]
	if build.RegistryID() != "reg-1" {
		t.Errorf("build.RegistryID() = %q, want reg-1", build.RegistryID())
	}
	services := build.BuildServices()
	if len(services) != 2 || services[0].ServiceName != "gateway" || services[0].ServiceModule != "gateway" || services[0].BuildName != "gateway-build" {
		t.Errorf("build.BuildServices() = %+v, want gateway/user with their modules", services)
	}

	if got := jobs[2].Env(); got != "prod" {
		t.Errorf("deploy.Env() = %q, want prod", got)
	}
	if got := jobs[2].ImageSourceJob(); got != "build" {
		t.Errorf("deploy.ImageSourceJob() = %q, want build", got)
	}
	// A deploy job that does not take its images from another job must not claim
	// to: source is what decides, not the presence of a job_name.
	if got := jobs[3].ImageSourceJob(); got != "" {
		t.Errorf("deploy-manual.ImageSourceJob() = %q, want empty when source is not fromjob", got)
	}

	// A job whose spec this client does not model reads as empty rather than
	// failing the whole read: the command has to be able to describe a workflow
	// that contains job types it does not act on.
	if got := jobs[1].BuildServices(); got != nil {
		t.Errorf("lint.BuildServices() = %v, want nil", got)
	}
	if got := jobs[1].Env(); got != "" {
		t.Errorf("lint.Env() = %q, want empty", got)
	}
}

func TestWorkflowDetailErrors(t *testing.T) {
	const path = "/openapi/workflows/custom/release-x/detail"

	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "non-2xx carries status and body", status: http.StatusBadRequest, body: `{"message":"工作流不存在"}`, want: "工作流不存在"},
		{name: "a non-JSON body is an error", status: http.StatusOK, body: `<html>502</html>`, want: "decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newRecorder(t, map[string]route{path: {status: tt.status, body: tt.body}})
			client := NewClient(srv.URL, "token", "p")

			if _, err := client.WorkflowDetail(context.Background(), "p", "release-x"); err == nil {
				t.Fatalf("WorkflowDetail() error = nil, want one containing %q", tt.want)
			} else if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.want)
			}
		})
	}

	// A workflow named in the path is escaped: an unescaped "/" would address a
	// different route entirely. The server matches the escaped form, so getting
	// an answer at all is half the check; the wire is the other half.
	srv, rec := newRecorder(t, map[string]route{"/openapi/workflows/custom/a%2Fb/detail": {status: http.StatusOK, body: `{}`}})
	client := NewClient(srv.URL, "token", "p")
	if _, err := client.WorkflowDetail(context.Background(), "p", "a/b"); err != nil {
		t.Fatalf("WorkflowDetail() with a slash in the name error = %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("%d requests reached the server, want 1 at the escaped path", rec.count())
	}
	if got, want := rec.requests[0].escaped, "/openapi/workflows/custom/a%2Fb/detail"; got != want {
		t.Errorf("escaped path = %q, want %q", got, want)
	}
}

// TestRegistryAddressResolvesBuildingAddress pins the exact string a build input
// must carry. Zadig matches the input's registry against addr + "/" + namespace,
// so this is the only value that resolves.
func TestRegistryAddressResolvesBuildingAddress(t *testing.T) {
	srv, rec := newRecorder(t, map[string]route{
		registryPath: {status: http.StatusOK, body: `[
			{"registry_id": "reg-1", "address": "ccr.example.com", "namespace": "ns"},
			{"registry_id": "reg-2", "address": "harbor.example.com", "namespace": ""}
		]`},
	})
	client := NewClient(srv.URL, "token", "p")

	got, err := client.RegistryAddress(context.Background(), "reg-1")
	if err != nil {
		t.Fatalf("RegistryAddress() error = %v", err)
	}
	if got != "ccr.example.com/ns" {
		t.Errorf("RegistryAddress() = %q, want ccr.example.com/ns", got)
	}

	// The separator survives an empty namespace, because that is how the server
	// builds its own key.
	if got, err := client.RegistryAddress(context.Background(), "reg-2"); err != nil || got != "harbor.example.com/" {
		t.Errorf("RegistryAddress(reg-2) = %q, %v, want harbor.example.com/", got, err)
	}

	if _, err := client.RegistryAddress(context.Background(), "reg-unknown"); err == nil {
		t.Error("RegistryAddress() with an unknown id error = nil, want one")
	} else if !strings.Contains(err.Error(), "reg-unknown") {
		t.Errorf("error = %q, want it to name the id that was not found", err.Error())
	}
	if _, err := client.RegistryAddress(context.Background(), "  "); err == nil {
		t.Error("RegistryAddress() with an empty id error = nil, want one")
	}

	if _, ok := rec.to(registryPath); !ok {
		t.Fatalf("no request reached %s", registryPath)
	}

	// The empty-id rejection happens before any request: there is nothing to look
	// up, and a round trip would only be answered by the same error.
	emptySrv, emptyRec := newRecorder(t, map[string]route{
		registryPath: {status: http.StatusOK, body: `[]`},
	})
	if _, err := NewClient(emptySrv.URL, "token", "p").RegistryAddress(context.Background(), "reg-1"); err == nil {
		t.Error("RegistryAddress() against an empty registry list error = nil, want one")
	}
	if emptyRec.count() == 0 {
		t.Error("no request reached the registry route")
	}
}

func TestExecuteCustomWorkflowErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		response string
		want     string
	}{
		{
			name:     "non-2xx carries status and body",
			status:   http.StatusBadRequest,
			response: `{"message":"服务不存在"}`,
			want:     "400",
		},
		{
			name:     "non-2xx body text is surfaced",
			status:   http.StatusBadRequest,
			response: `{"message":"服务不存在"}`,
			want:     "服务不存在",
		},
		{
			name:     "a success without a task id is an error",
			status:   http.StatusOK,
			response: `{"message":"ok"}`,
			want:     "task_id",
		},
		{
			name:     "a non-JSON success is an error",
			status:   http.StatusOK,
			response: `<html>gateway timeout</html>`,
			want:     "decode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newTaskServer(t, tt.status, tt.response)
			client := NewClient(srv.URL, "token", "p")

			_, err := client.ExecuteCustomWorkflow(context.Background(), &ExecuteCustomWorkflowRequest{
				ProjectName: "p", WorkflowName: "w",
				Inputs: []JobInput{{JobName: "build", JobType: JobTypeBuild}},
			})
			if err == nil {
				t.Fatalf("ExecuteCustomWorkflow() error = nil, want one containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestExecuteCustomWorkflowRejectsIncompleteRequest(t *testing.T) {
	srv, rec := newTaskServer(t, http.StatusOK, `{"task_id":1}`)
	client := NewClient(srv.URL, "token", "p")

	for _, req := range []*ExecuteCustomWorkflowRequest{
		nil,
		{WorkflowName: "w", Inputs: []JobInput{{JobName: "build"}}},
		{ProjectName: "p", Inputs: []JobInput{{JobName: "build"}}},
		// A task that names no job would ask Zadig for a task that runs nothing,
		// which is never what a caller meant.
		{ProjectName: "p", WorkflowName: "w"},
	} {
		if _, err := client.ExecuteCustomWorkflow(context.Background(), req); err == nil {
			t.Errorf("ExecuteCustomWorkflow(%+v) error = nil, want one", req)
		}
	}

	if rec.count() != 0 {
		t.Errorf("%d requests reached the server, want none", rec.count())
	}
}

func TestExecuteCustomWorkflowUnreachableEndpoint(t *testing.T) {
	srv, _ := newTaskServer(t, http.StatusOK, `{}`)
	url := srv.URL
	srv.Close()

	client := NewClient(url, "token", "p")
	_, err := client.ExecuteCustomWorkflow(context.Background(), &ExecuteCustomWorkflowRequest{
		ProjectName: "p", WorkflowName: "w",
		Inputs: []JobInput{{JobName: "build"}},
	})
	if err == nil {
		t.Fatal("ExecuteCustomWorkflow() error = nil, want a transport error")
	}
	if !strings.Contains(err.Error(), customTaskPath) {
		t.Errorf("error = %q, want it to name the path that failed", err.Error())
	}
}

func TestConsoleURL(t *testing.T) {
	client := NewClient("https://zadig.example.com/", "token", "")
	got := client.ConsoleURL("my project", "release/x", 42)
	if !strings.Contains(got, "/my%20project/") {
		// An escaped segment, not a raw one: an unescaped "/" or space would
		// build a link to a different page.
		t.Errorf("ConsoleURL() = %q, want the project segment escaped", got)
	}
	if !strings.HasSuffix(got, "/42") {
		t.Errorf("ConsoleURL() = %q, want it to end in the task id", got)
	}
	if strings.Contains(got, "release/x") {
		t.Errorf("ConsoleURL() = %q, want the workflow segment escaped", got)
	}
	if !strings.Contains(got, "/pipelines/custom/") {
		// A custom workflow's task lives under the "custom" segment; the product
		// workflows this client no longer addresses use another one.
		t.Errorf("ConsoleURL() = %q, want the custom-workflow segment", got)
	}

	if got := client.ConsoleURL("", "w", 1); got != "" {
		t.Errorf("ConsoleURL() with no project = %q, want empty", got)
	}
}

func TestDefaultProject(t *testing.T) {
	if got := NewClient("https://zadig.example.com", "token", " my-project ").DefaultProject(); got != "my-project" {
		t.Errorf("DefaultProject() = %q, want my-project", got)
	}
}

// TestNoopClient pins the "no Zadig configured" contract: every call fails with
// a message naming the key to set, rather than dereferencing a nil client.
func TestNoopClient(t *testing.T) {
	client := NewClient("", "token", "my-project")

	if _, err := client.ExecuteCustomWorkflow(context.Background(), &ExecuteCustomWorkflowRequest{
		ProjectName: "p", WorkflowName: "w",
		Inputs: []JobInput{{JobName: "build"}},
	}); err == nil {
		t.Fatal("ExecuteCustomWorkflow() error = nil, want one")
	} else if !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("error = %q, want it to name the missing endpoint", err.Error())
	}

	if _, err := client.WorkflowDetail(context.Background(), "p", "w"); err == nil {
		t.Error("WorkflowDetail() error = nil, want one")
	} else if !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("error = %q, want it to name the missing endpoint", err.Error())
	}

	if _, err := client.RegistryAddress(context.Background(), "reg-1"); err == nil {
		t.Error("RegistryAddress() error = nil, want one")
	} else if !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("error = %q, want it to name the missing endpoint", err.Error())
	}

	if got := client.ConsoleURL("p", "w", 1); got != "" {
		t.Errorf("ConsoleURL() = %q, want empty without an endpoint", got)
	}
	if got := client.DefaultProject(); got != "" {
		t.Errorf("DefaultProject() = %q, want empty without an endpoint", got)
	}
	if client.Configured() {
		t.Error("Configured() = true without an endpoint")
	}

	if !NewClient("https://zadig.example.com", "token", "").Configured() {
		t.Error("Configured() = false with an endpoint set")
	}
}
