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
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/cmdb"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
)

func cardTestPod(name, namespace string) *PodMetrics {
	return &PodMetrics{
		PodName:     name,
		Namespace:   namespace,
		ClusterName: "fz-prod",
		PodIP:       "10.0.0.1",
		Phase:       "Running",
		Containers:  []ContainerInfo{{Name: "app", Image: "example/app:v1"}},
	}
}

// cardTestRichPod carries every value the fields render from, so a test can
// check the table's whole row set.
func cardTestRichPod() *PodMetrics {
	pod := cardTestPod("gateway-1", "prod")
	pod.Node, pod.HostIP = "node-1", "10.0.1.1"
	pod.WorkloadType = "ReplicaSet"
	pod.CreatedAt = "2026-01-01T00:00:00Z"
	pod.Restarts = 3
	pod.ServiceAccount = "default"
	cores, bytes, rate := 0.5, float64(64*1024*1024), float64(1024)
	pod.CPUUsageCores, pod.CPURequestCores, pod.CPULimitCores = &cores, &cores, &cores
	pod.MemUsageBytes, pod.MemRequestBytes, pod.MemLimitBytes = &bytes, &bytes, &bytes
	pod.NetRxBytesPerSec, pod.NetTxBytesPerSec = &rate, &rate
	pod.Containers = append(pod.Containers, ContainerInfo{Name: "sidecar", Image: "example/sidecar:v2"})
	pod.CMDB = &PodCMDBInfo{
		Summary: &cmdb.ServiceSummaryResponse{Data: []cmdb.ServiceSummaryGroup{
			{GroupName: "基础信息", Attrs: []cmdb.ServiceSummaryAttr{{Label: "实例ID", Value: "i-1"}}},
		}},
		Network: &cmdb.NetworkResources{
			VPC:     &cmdb.VPCInfo{ID: "vpc-1", Name: "prod-vpc", CIDR: "10.0.0.0/16"},
			Subnets: []*cmdb.SubnetInfo{{ID: "subnet-1", CIDR: "10.0.1.0/24", AvailabilityZone: "az-a"}},
		},
	}
	return pod
}

func newCardTestStore() *podCardStore {
	return &podCardStore{m: make(map[string]*podSnapshot)}
}

func TestParsePodSelectAction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  string
		wantOK  bool
		wantIdx int
		wantTok string
	}{
		{name: "select", action: "pod:sel:abc123:2", wantOK: true, wantIdx: 2, wantTok: "abc123"},
		{name: "select zero", action: "pod:sel:abc123:0", wantOK: true, wantTok: "abc123"},
		{name: "unknown op", action: "pod:jump:abc123:1"},
		{name: "page op is no longer accepted", action: "pod:pg:abc123:0:1"},
		{name: "missing index", action: "pod:sel:abc123"},
		{name: "extra field", action: "pod:sel:abc123:1:2"},
		{name: "negative index", action: "pod:sel:abc123:-1"},
		{name: "non-numeric index", action: "pod:sel:abc123:x"},
		{name: "missing token", action: "pod:sel::1"},
		{name: "not a pod action", action: "nav:/help"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx, tok, ok := parsePodSelectAction(tc.action)
			if ok != tc.wantOK {
				t.Fatalf("parsePodSelectAction(%q) ok = %v, want %v", tc.action, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if idx != tc.wantIdx || tok != tc.wantTok {
				t.Errorf("parsePodSelectAction(%q) = (%d, %q), want (%d, %q)",
					tc.action, idx, tok, tc.wantIdx, tc.wantTok)
			}
		})
	}
}

func TestPodCardStoreRoundTrip(t *testing.T) {
	store := newCardTestStore()
	snap := store.put([]*PodMetrics{cardTestPod("gateway-1", "prod")})

	if snap.token == "" {
		t.Fatal("put returned an empty token")
	}
	got, ok := store.get(snap.token)
	if !ok {
		t.Fatal("get missed a snapshot that was just stored")
	}
	if len(got.pods) != 1 || got.pods[0].PodName != "gateway-1" {
		t.Errorf("snapshot = %+v, want the stored pod", got)
	}
	if _, ok := store.get("not-a-token"); ok {
		t.Error("get returned a snapshot for an unknown token")
	}
}

// TestPodCardStoreDropsExpired pins that a snapshot older than the TTL is a miss:
// the metrics behind it are stale, and a card that redrew from them would show
// values nobody could tell were old.
func TestPodCardStoreDropsExpired(t *testing.T) {
	store := newCardTestStore()
	snap := store.put([]*PodMetrics{cardTestPod("gateway-1", "prod")})
	snap.created = time.Now().Add(-podCardTTL - time.Minute)

	if _, ok := store.get(snap.token); ok {
		t.Error("get returned an expired snapshot")
	}
}

// TestPodCardStoreBoundsEntries pins the size cap: a burst of /pod queries
// cannot pin an unbounded number of pod lists in memory.
func TestPodCardStoreBoundsEntries(t *testing.T) {
	store := newCardTestStore()

	var first string
	for i := 0; i <= podCardMaxEntries; i++ {
		snap := store.put([]*PodMetrics{cardTestPod(fmt.Sprintf("pod-%d", i), "prod")})
		if i == 0 {
			first = snap.token
		}
	}

	if _, ok := store.get(first); ok {
		t.Error("the oldest snapshot survived past the entry cap")
	}
	if len(store.m) > podCardMaxEntries {
		t.Errorf("store holds %d snapshots, want at most %d", len(store.m), podCardMaxEntries)
	}
}

// TestPodKVFieldsAreFlatRows pins the shape the table needs: every container and
// every CMDB attribute is a row of its own, with the pod's identity first.
func TestPodKVFieldsAreFlatRows(t *testing.T) {
	fields := podKVFields(cardTestRichPod())

	var values []string
	keys := map[string]int{}
	for _, f := range fields {
		keys[f.Key]++
		values = append(values, f.Value)
	}
	if fields[0].Key != "Pod" || fields[0].Value != "gateway-1" {
		t.Errorf("first field = %+v, want the pod's name", fields[0])
	}
	if keys["容器"] != 2 {
		t.Errorf("容器 rows = %d, want one per container (2)", keys["容器"])
	}
	if keys["基础信息/实例ID"] != 1 {
		t.Errorf("CMDB attribute is not its own row: keys = %v", keys)
	}
	if keys["VPC"] != 1 || keys["服务子网"] != 1 {
		t.Errorf("CMDB network rows missing: keys = %v", keys)
	}

	joined := strings.Join(values, "\n")
	for _, want := range []string{"app — example/app:v1", "sidecar — example/sidecar:v2", "i-1", "prod-vpc"} {
		if !strings.Contains(joined, want) {
			t.Errorf("field values do not contain %q", want)
		}
	}
}

// TestPodUsageRendersABar pins the at-a-glance form of a usage row, and that a
// pod with no request keeps the plain value (nothing to fill the bar against).
func TestPodUsageRendersABar(t *testing.T) {
	used, request := 0.5, 1.0
	got := podUsage(&used, &request, "500.0 m")
	if !strings.HasPrefix(got, "▰▰▰▰▱▱▱▱ 50.0%") {
		t.Errorf("podUsage = %q, want a half-filled bar followed by the percentage", got)
	}
	if !strings.Contains(got, "500.0 m") {
		t.Errorf("podUsage = %q, want it to keep the rendered value", got)
	}

	if got := podUsage(&used, nil, "500.0 m"); got != "500.0 m" {
		t.Errorf("podUsage without a request = %q, want the plain value", got)
	}
	if got := podUsage(nil, &request, ""); got != "" {
		t.Errorf("podUsage without a usage = %q, want empty", got)
	}
}

// TestPodKeyColumnWidth pins the sizing rule: the column is as wide as its widest
// key, Chinese counted double (a Chinese key must not be sized like Pod), and never outside
// the range that keeps both columns readable.
func TestPodKeyColumnWidth(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		keys   []string
		want   string
	}{
		// 4 units x 7px + 16px = 44px, raised to the platform's 80px minimum.
		{name: "short keys take the minimum", header: "字段", keys: []string{"Pod", "集群"}, want: "80px"},
		// "工作负载类型" (a 6-character key) is 12 units: 84px + 16px.
		{name: "the widest key sets the width", header: "字段", keys: []string{"Pod", "工作负载类型"}, want: "100px"},
		// The header shares the column, so it can be the widest thing in it:
		// "字段最大值" (a 5-character key) is 10 units -> 70px + 16px.
		{name: "a long header counts too", header: "字段最大值", keys: []string{"Pod"}, want: "86px"},
		// A keys row this long would otherwise squeeze the values off a phone-width
		// card; past the cap the key wraps, which row_height auto absorbs.
		{name: "a very long key stops at the cap", header: "字段", keys: []string{strings.Repeat("字", 40)}, want: "240px"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := make([]podField, 0, len(tc.keys))
			for _, key := range tc.keys {
				fields = append(fields, podField{Key: key, Value: "v"})
			}
			if got := podKeyColumnWidth(tc.header, fields); got != tc.want {
				t.Errorf("podKeyColumnWidth(%q, %v) = %q, want %q", tc.header, tc.keys, got, tc.want)
			}
		})
	}
}

// podCardTable returns the table every /pod card is built around. Its absence is
// the failure worth naming: the rows are the card.
func podCardTable(t *testing.T, card *larkcard.Card) larkcard.CardTable {
	t.Helper()

	for _, elem := range card.Elements {
		if table, ok := elem.(larkcard.CardTable); ok {
			return table
		}
	}
	t.Fatalf("card carries no table: %+v", card.Elements)
	return larkcard.CardTable{}
}

// TestRenderPodCardUsesANativeTable pins the card's central shape: one two-column
// table (field / value), the rows rendered from the pod's fields, and — for a
// multi-pod match — the selector above it.
func TestRenderPodCardUsesANativeTable(t *testing.T) {
	pod := cardTestRichPod()
	snap := &podSnapshot{
		token: "tok12345",
		pods:  []*PodMetrics{pod, cardTestPod("gateway-2", "test")},
	}

	card := renderPodCard(snap, 1)

	var sel *larkcard.CardSelect
	for _, elem := range card.Elements {
		if e, ok := elem.(larkcard.CardSelect); ok {
			sel = &e
		}
	}
	if sel == nil {
		t.Fatal("the card has no selector, so a many-pod match cannot be navigated")
	}
	if len(sel.Options) != 2 || sel.InitValue != sel.Options[1].Value {
		t.Fatalf("selector = %+v, want the second pod preselected", sel)
	}
	idx, token, ok := parsePodSelectAction(strings.TrimPrefix(sel.Options[1].Value, "nav:"))
	if !ok || token != "tok12345" || idx != 1 {
		t.Errorf("option value parses to (%d, %q, %v), want the second pod of tok12345", idx, token, ok)
	}

	table := podCardTable(t, card)
	if len(table.Columns) != 2 || table.Columns[0].Name != "field" || table.Columns[1].Name != "value" {
		t.Fatalf("columns = %+v, want the key/value pair", table.Columns)
	}
	// The key column asks for exactly what its widest key needs, and the value
	// column asks for nothing: that is what leaves it the rest of the card.
	fields := podKVFields(snap.pods[1])
	if want := podKeyColumnWidth(podTableFieldHeader, fields); table.Columns[0].Width != want {
		t.Errorf("key column width = %q, want %q", table.Columns[0].Width, want)
	}
	if table.Columns[1].Width != "" {
		t.Errorf("value column width = %q, want it left unset so it takes the remainder", table.Columns[1].Width)
	}
	if len(table.Rows) != len(fields) {
		t.Errorf("table has %d rows, want one per field (%d)", len(table.Rows), len(fields))
	}
	if table.Rows[0]["field"] != "Pod" || table.Rows[0]["value"] != "gateway-2" {
		t.Errorf("first row = %v, want the selected pod", table.Rows[0])
	}
	if table.PageSize != podTablePageSize || !table.FreezeFirstColumn {
		t.Errorf("table = %+v, want page size %d and a frozen first column", table, podTablePageSize)
	}
	// The card carries no header: the pod's name and status are rows, and a title
	// bar above them only repeats what the table already says.
	if card.Header != nil {
		t.Errorf("header = %+v, want no card header", card.Header)
	}

	// The JSON is what Feishu actually receives; a card the schema check rejects
	// would be refused at send time with an error the user cannot act on.
	rendered := larkcard.RenderCard(card, "session-key")
	if violations := larkcard.Card2Violations([]byte(rendered)); len(violations) > 0 {
		t.Errorf("rendered card violates card 2.0: %v", violations)
	}
	for _, want := range []string{`"tag":"table"`, `"select_static"`, `"session_key":"session-key"`,
		`"width":"` + table.Columns[0].Width + `"`, "gateway-2"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered card does not contain %s", want)
		}
	}
}

// TestRenderPodCardSinglePodHasNoSelector keeps the common case clean: one match
// is a read-out, not a choice.
func TestRenderPodCardSinglePodHasNoSelector(t *testing.T) {
	snap := &podSnapshot{token: "tok12345", pods: []*PodMetrics{cardTestPod("gateway-1", "prod")}}

	card := renderPodCard(snap, 0)
	for _, elem := range card.Elements {
		if _, ok := elem.(larkcard.CardSelect); ok {
			t.Error("a single-pod card rendered a selector")
		}
	}
	if strings.Contains(larkcard.RenderCard(card, "sk"), "按钮") || card.HasButtons() {
		t.Error("the card carries buttons; the table pages itself")
	}
}

func TestHandlePodCardAction(t *testing.T) {
	store := newCardTestStore()
	snap := store.put([]*PodMetrics{cardTestPod("gateway-1", "prod"), cardTestPod("gateway-2", "prod")})

	// A click on a result this process still holds redraws that result.
	card, ok := handlePodCardAction(context.Background(), store, "pod:sel:"+snap.token+":1")
	if !ok {
		t.Fatal("a well-formed action was not claimed")
	}
	if rows := podCardTable(t, card).Rows; rows[0]["value"] != "gateway-2" {
		t.Errorf("first row = %v, want the pod the action selected", rows[0])
	}

	// A click on a card whose snapshot is gone still has to be claimed: falling
	// through would run "pod:sel:..." as a command.
	expired, ok := handlePodCardAction(context.Background(), store, "pod:sel:missing:0")
	if !ok {
		t.Fatal("an action naming an unknown result was not claimed")
	}
	if !strings.Contains(renderCardMarkdown(expired), "重新执行") {
		t.Errorf("expired card = %q, want it to say the result expired", renderCardMarkdown(expired))
	}
	if expired.HasButtons() {
		t.Error("the expired card carries controls, all of which would fail the same way")
	}

	if _, ok := handlePodCardAction(context.Background(), store, "pod:nonsense"); !ok {
		t.Error("a malformed pod action was not claimed")
	}
	if _, ok := handlePodCardAction(context.Background(), store, "help:pod"); ok {
		t.Error("an action that is not a pod action was claimed")
	}
}

func renderCardMarkdown(card *larkcard.Card) string {
	var parts []string
	for _, elem := range card.Elements {
		if md, ok := elem.(larkcard.CardMarkdown); ok {
			parts = append(parts, md.Content)
		}
	}
	return strings.Join(parts, "\n")
}

// TestHandlePodCardActionExportedEntry is the guard on the function the card
// layer actually calls: it must agree with the parser about which actions belong
// to a pod card.
func TestHandlePodCardActionExportedEntry(t *testing.T) {
	if _, ok := HandlePodCardAction(context.Background(), "pod:sel:unknown-token:0"); !ok {
		t.Error("HandlePodCardAction did not claim a pod action")
	}
	if _, ok := HandlePodCardAction(context.Background(), "nav:/help"); ok {
		t.Error("HandlePodCardAction claimed an action that is not a pod card's")
	}
}

// TestRenderPodCardSurvivesJSONMarshal pins that the rendered map is
// marshallable — the card is sent as JSON, and a value that fails to marshal
// reaches Feishu as nothing at all.
func TestRenderPodCardSurvivesJSONMarshal(t *testing.T) {
	snap := &podSnapshot{token: "tok", pods: []*PodMetrics{cardTestRichPod()}}
	rendered := larkcard.RenderCardMap(renderPodCard(snap, 0), "sk")
	if _, err := json.Marshal(rendered); err != nil {
		t.Fatalf("marshalling the rendered card failed: %v", err)
	}
}
