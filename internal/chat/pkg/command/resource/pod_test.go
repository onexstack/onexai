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

package resource

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/common/model"

	"github.com/onexstack/onexai/internal/chat/pkg/clientset"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/cmdb"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/prometheus"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// stubPrometheus answers a query with the canned vector whose metric name it
// contains, and records every query it receives. The queries are built
// dynamically, so tests assert on the recorded strings instead of matching
// exact queries.
type stubPrometheus struct {
	answers    map[string]model.Vector
	configured bool

	// mu guards queries: the command fans the enrichment queries out over
	// goroutines, and the stub records every one of them.
	mu      sync.Mutex
	queries []string
}

func (s *stubPrometheus) Configured() bool { return s.configured }

func (s *stubPrometheus) Query(_ context.Context, query string) (model.Vector, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()

	keys := make([]string, 0, len(s.answers))
	for k := range s.answers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.Contains(query, k) {
			return s.answers[k], nil
		}
	}
	return model.Vector{}, nil
}

// stubClientset supplies the one client the command reaches for.
type stubClientset struct {
	clientset.Interface
	prometheus prometheus.Interface
	cmdb       cmdb.Interface
}

func (s *stubClientset) Prometheus() prometheus.Interface { return s.prometheus }
func (s *stubClientset) CMDB() cmdb.Interface             { return s.cmdb }

// stubCMDB records the pods it was asked about; embedding the interface keeps
// the methods the command never calls out of the test.
type stubCMDB struct {
	cmdb.Interface
	searched []string
}

func (s *stubCMDB) SearchPodNode(_ context.Context, podName, _, _ string) (string, error) {
	s.searched = append(s.searched, podName)
	return "node-1", nil
}

func (s *stubCMDB) GetNodeSummary(_ context.Context, _ cmdb.ViewID, _ string) (*cmdb.ServiceSummaryResponse, error) {
	return &cmdb.ServiceSummaryResponse{Data: []cmdb.ServiceSummaryGroup{}}, nil
}

func (s *stubCMDB) GetNetworkResources(_ context.Context, _ cmdb.ViewID, _ string) (*cmdb.NetworkResources, error) {
	return &cmdb.NetworkResources{}, nil
}

// sample builds one vector sample with the given label set and value.
func sample(value float64, labels map[string]string) *model.Sample {
	metric := make(model.Metric, len(labels))
	for k, v := range labels {
		metric[model.LabelName(k)] = model.LabelValue(v)
	}
	return &model.Sample{Metric: metric, Value: model.SampleValue(value)}
}

func podInfoVector(pods ...map[string]string) model.Vector {
	vector := make(model.Vector, 0, len(pods))
	for _, labels := range pods {
		vector = append(vector, sample(1, labels))
	}
	return vector
}

// runPod executes the command with the given static runtime and arguments and
// returns both the error and what the responder captured, so a test can assert
// that a failure was reported to the caller rather than only returned.
func runPod(t *testing.T, sr *runtime.StaticRuntime, args ...string) (error, *runtime.CommandResult) {
	t.Helper()

	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{Source: runtime.RequestSourceAPI})

	cmd := NewPodCmd(sr, out)
	cmd.SetArgs(args)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.ExecuteContext(context.Background())
	return err, out.Result()
}

func TestBuildPodQuery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pod       string
		namespace string
		cluster   string
		want      string
	}{
		{
			name: "name only",
			pod:  "gateway",
			want: `kube_pod_info{pod=~".*gateway.*"}`,
		},
		{
			name:      "all filters",
			pod:       "gateway",
			namespace: "prod",
			cluster:   "fz-prod",
			want:      `kube_pod_info{pod=~".*gateway.*",namespace=~".*prod.*",cluster=~".*fz-prod.*"}`,
		},
		{
			name: "no filters",
			want: "kube_pod_info",
		},
		{
			name: "quote is escaped",
			pod:  `web"svc`,
			want: `kube_pod_info{pod=~".*web\"svc.*"}`,
		},
		{
			name: "backslash is escaped",
			pod:  `web\svc`,
			want: `kube_pod_info{pod=~".*web\\svc.*"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildPodQuery(tc.pod, tc.namespace, tc.cluster)
			if got != tc.want {
				t.Errorf("buildPodQuery() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPodQueriesPrometheusWithClusterLabel pins that the cluster filter uses
// the `cluster` label carried by this deployment's kube-state-metrics, not the
// `eks_cluster_name` label of the opsassist deployment it was ported from.
func TestPodQueriesPrometheusWithClusterLabel(t *testing.T) {
	stub := &stubPrometheus{
		configured: true,
		answers: map[string]model.Vector{
			"kube_pod_info": podInfoVector(
				map[string]string{"pod": "gateway-1", "namespace": "prod", "cluster": "fz-prod", "pod_ip": "10.0.0.1"},
			),
			"kube_pod_status_phase": {sample(1, map[string]string{
				"pod": "gateway-1", "namespace": "prod", "phase": "Running",
			})},
			"kube_pod_container_info": {sample(1, map[string]string{
				"pod": "gateway-1", "namespace": "prod", "container": "app", "image": "example/app:v1",
			})},
		},
	}

	err, res := runPod(t, &runtime.StaticRuntime{Clientset: &stubClientset{prometheus: stub}},
		"gateway", "-n", "prod", "-c", "fz-prod")
	if err != nil {
		t.Fatalf("ExecuteContext() = %v, want nil", err)
	}

	if len(stub.queries) == 0 {
		t.Fatal("no query reached Prometheus")
	}
	first := stub.queries[0]
	for _, want := range []string{`pod=~".*gateway.*"`, `namespace=~".*prod.*"`, `cluster=~".*fz-prod.*"`} {
		if !strings.Contains(first, want) {
			t.Errorf("kube_pod_info query = %q, want it to contain %q", first, want)
		}
	}

	content := res.Markdown.Content
	for _, want := range []string{"gateway-1", "fz-prod", "10.0.0.1", "Running", "example/app:v1"} {
		if !strings.Contains(content, want) {
			t.Errorf("markdown = %q, want it to contain %q", content, want)
		}
	}
	if res.PlainText == "" {
		t.Error("PlainText is empty; the REST/MCP surface falls back to it")
	}
}

func TestPodNoMatches(t *testing.T) {
	stub := &stubPrometheus{configured: true, answers: map[string]model.Vector{}}

	err, res := runPod(t, &runtime.StaticRuntime{Clientset: &stubClientset{prometheus: stub}}, "nothing-here")
	if err != nil {
		t.Fatalf("ExecuteContext() = %v, want nil", err)
	}
	if !strings.Contains(res.Markdown.Content, "未查询到") {
		t.Errorf("markdown = %q, want the not-found message", res.Markdown.Content)
	}
}

func TestPodWithoutPrometheusIsAUserError(t *testing.T) {
	stub := &stubPrometheus{configured: false}

	err, _ := runPod(t, &runtime.StaticRuntime{Clientset: &stubClientset{prometheus: stub}}, "gateway-1")
	if err == nil {
		t.Fatal("ExecuteContext() = nil, want an error")
	}
	if !runtime.IsUserError(err) {
		t.Errorf("error = %v, want a UserError", err)
	}
	if !strings.Contains(err.Error(), "prometheus-endpoint") {
		t.Errorf("error = %v, want it to name the missing configuration key", err)
	}
}

func TestPodRejectsAnOverBroadQuery(t *testing.T) {
	pods := make([]map[string]string, 0, maxMatchedPods+1)
	for i := 0; i <= maxMatchedPods; i++ {
		pods = append(pods, map[string]string{"pod": fmt.Sprintf("web-%d", i), "namespace": "prod"})
	}
	stub := &stubPrometheus{
		configured: true,
		answers:    map[string]model.Vector{"kube_pod_info": podInfoVector(pods...)},
	}

	err, _ := runPod(t, &runtime.StaticRuntime{Clientset: &stubClientset{prometheus: stub}}, "web")
	if err == nil {
		t.Fatal("ExecuteContext() = nil, want an error")
	}
	if !runtime.IsUserError(err) {
		t.Errorf("error = %v, want a UserError", err)
	}
}

// TestPodSkipsCMDBByDefault pins the "kept but disabled" contract: the CMDB code
// is reachable, but nothing calls it unless the deployment turns it on.
func TestPodSkipsCMDBByDefault(t *testing.T) {
	stub := &stubPrometheus{
		configured: true,
		answers: map[string]model.Vector{
			"kube_pod_info": podInfoVector(map[string]string{"pod": "gateway-1", "namespace": "prod"}),
		},
	}
	cmdbStub := &stubCMDB{}

	err, _ := runPod(t, &runtime.StaticRuntime{
		Clientset:   &stubClientset{prometheus: stub, cmdb: cmdbStub},
		CMDBEnabled: false,
	}, "gateway-1")
	if err != nil {
		t.Fatalf("ExecuteContext() = %v, want nil", err)
	}
	if len(cmdbStub.searched) != 0 {
		t.Errorf("CMDB was queried %d time(s) with CMDBEnabled=false", len(cmdbStub.searched))
	}
}

// TestPodEnrichesFromCMDBWhenEnabled keeps the retained CMDB path from rotting.
func TestPodEnrichesFromCMDBWhenEnabled(t *testing.T) {
	stub := &stubPrometheus{
		configured: true,
		answers: map[string]model.Vector{
			"kube_pod_info": podInfoVector(map[string]string{"pod": "gateway-1", "namespace": "prod"}),
		},
	}
	cmdbStub := &stubCMDB{}

	_, res := runPod(t, &runtime.StaticRuntime{
		Clientset:   &stubClientset{prometheus: stub, cmdb: cmdbStub},
		CMDBEnabled: true,
	}, "gateway-1")

	if len(cmdbStub.searched) != 1 || cmdbStub.searched[0] != "gateway-1" {
		t.Errorf("CMDB searched %v, want [gateway-1]", cmdbStub.searched)
	}
	if res == nil {
		t.Fatal("no result captured")
	}
}
