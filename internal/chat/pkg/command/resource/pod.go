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
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/prometheus/common/model"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/cmdb"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

const (
	// maxMatchedPods bounds how many pods one query may return. The limit
	// exists so a query that matched a whole namespace is refused with a
	// request to narrow it, rather than answered with an unreadable wall.
	maxMatchedPods = 50
	// podListTimeout bounds the kube_pod_info query.
	podListTimeout = 15 * time.Second
	// podMetricsTimeout bounds the metric enrichment fan-out.
	podMetricsTimeout = 15 * time.Second
)

var (
	podLong = templates.LongDesc(`
		该工具用于【查询 Kubernetes 集群中 Pod 的状态、基础信息及排障信息】。

		【触发场景】：
		当用户需要排查 Pod 相关问题时，如"查一下 xxx Pod 状态"、"xxx 服务的 Pod 有异常吗"、
		"帮我看看 production 命名空间下的 xxx Pod"，请调用此工具。

		【参数说明】：
		- <pod名称>（位置参数，必填）：目标 Pod 的名称，支持模糊匹配。
		- --namespace / -n：Pod 所在的命名空间，不填时默认搜索所有命名空间。
		- --cluster / -c：限定集群名称，不填时搜索全部。

		可查询的 Pod 信息如下：
		- 集群名称、命名空间、Pod名称、Pod IP、节点、状态、工作负载类型、创建时间、重启次数
		- 容器名称、容器镜像、CPU/内存 使用量、Request、Limit、服务账号、网络收发速率

		数据来源为 Prometheus 指标（kube-state-metrics / cAdvisor）。当部署开启了
		CMDB 富化（backend.cmdb-enabled）时，还会附加 VPC、服务子网、服务安全组等信息。`)

	podExample = templates.Examples(`
		# 查询名为 "gateway-xxx" 的 Pod 状态（搜索所有命名空间）
		/pod gateway-xxx

		# 查询指定命名空间下的 Pod（支持模糊匹配）
		/pod gateway-xxx -n prod

		# 结合集群名进行精准联合查询
		/pod gateway-xxx -c my-cluster -n test`)
)

// PodOptions defines configuration for the "pod" command.
type PodOptions struct {
	out runtime.Responder
	sr  *runtime.StaticRuntime

	PodName     string
	Namespace   string
	ClusterName string
}

// ContainerInfo is one container of a pod, as reported by kube_pod_container_info.
type ContainerInfo struct {
	Name  string
	Image string
}

// PodCMDBInfo is the CMDB enrichment of a pod. It is populated only when the
// deployment enables backend.cmdb-enabled.
type PodCMDBInfo struct {
	Summary *cmdb.ServiceSummaryResponse
	Network *cmdb.NetworkResources
}

// PodMetrics is one pod as read from Prometheus, together with the fields the
// metric enrichment fills in.
type PodMetrics struct {
	PodName      string
	ServiceName  string
	Namespace    string
	ClusterName  string
	PodIP        string
	HostIP       string
	Node         string
	UUID         string
	WorkloadType string

	Phase          string
	CreatedAt      string
	Restarts       int32
	ServiceAccount string
	Containers     []ContainerInfo

	// Resource usage is kept as numbers, not pre-rendered strings: a percentage
	// is only computable when usage and request share a unit, and an idle pod's
	// usage (fractions of a core) has to survive to be rendered at all.
	CPUUsageCores   *float64
	CPURequestCores *float64
	CPULimitCores   *float64
	MemUsageBytes   *float64
	MemRequestBytes *float64
	MemLimitBytes   *float64

	NetRxBytesPerSec *float64
	NetTxBytesPerSec *float64

	// CMDB is filled only when CMDB enrichment is enabled.
	CMDB *PodCMDBInfo
}

// NewPodCmd creates the "pod" command.
//
// The pod list and its metrics come from Prometheus. The CMDB enrichment path
// is kept but opt-in (backend.cmdb-enabled), so this command answers from
// metrics alone on a deployment that has not turned CMDB on.
func NewPodCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &PodOptions{out: out, sr: sr}

	cmd := &cobra.Command{
		Use:                   "pod <pod_name>",
		Short:                 "查询 k8s Pod 集群、命名空间、IP 地址及资源用量",
		Long:                  podLong,
		Example:               podExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		Annotations: map[string]string{
			cobrax.AnnotationArgPrefix + "0": "【必填参数】目标 Pod 的名称（支持部分匹配）。",
			chat.MCPTool:                     chat.True,
			chat.ChatGroup:                   chat.ChatGroupJob,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd.Context(), args); err != nil {
				return err
			}
			if err := o.Validate(cmd.Context(), args); err != nil {
				return err
			}
			return o.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVarP(&o.ClusterName, "cluster", "c", "", "限定集群名称（支持模糊匹配，不指定则搜索全部）")
	cmd.Flags().StringVarP(&o.Namespace, "namespace", "n", "", "限定命名空间（支持模糊匹配，不指定则搜索全部）")

	return cmd
}

// Complete captures the positional pod name.
func (o *PodOptions) Complete(ctx context.Context, args []string) error {
	o.PodName = args[0]
	return nil
}

// Validate ensures the pod name is present.
func (o *PodOptions) Validate(ctx context.Context, args []string) error {
	if o.PodName == "" {
		return runtime.NewUserError("必须提供 Pod 名称")
	}
	return nil
}

// Run lists the matching pods from Prometheus, enriches them with metrics, and
// renders the result. CMDB enrichment runs only when it is enabled.
func (o *PodOptions) Run(ctx context.Context) error {
	pods, err := o.listPodsFromPrometheus(ctx)
	if err != nil {
		return err
	}

	if len(pods) == 0 {
		return o.out.SendResult(ctx, &runtime.CommandResult{
			PlainText: "未查询到该 Pod 的信息。",
			Markdown: &runtime.MarkdownContent{
				Title:   "Pod 信息：" + o.PodName,
				Content: "未查询到该 Pod 的信息。",
				Color:   "orange",
			},
		})
	}

	o.enrichMetrics(ctx, pods)

	if o.sr != nil && o.sr.CMDBEnabled {
		if err := o.enrichWithCMDB(ctx, pods); err != nil {
			// CMDB is an enrichment on top of the metric data: a failure here
			// leaves the answer complete except for the VPC/subnet rows.
			slog.WarnContext(ctx, "CMDB enrichment for /pod failed", "error", err)
		}
	}

	content := formatPodMarkdown(pods)
	res := &runtime.CommandResult{
		Raw: pods,
		Markdown: &runtime.MarkdownContent{
			Title:   "Pod 信息：" + o.PodName,
			Content: content,
			Color:   "blue",
		},
		PlainText: content,
	}

	// A Lark caller gets a card it can navigate — one card with a selector and
	// the selected pod's rows, instead of the wall of text a many-pod match
	// renders to. Every other surface (REST, MCP, the agent tool path) keeps the
	// text, which is why Raw and Markdown are still set.
	if dr := runtime.From(ctx); dr.HasLarkContext() {
		res.Interactive = renderPodCard(podCards.put(pods), 0)
	}

	return o.out.SendResult(ctx, res)
}

// listPodsFromPrometheus queries kube_pod_info for the pods matching the
// command's filters.
func (o *PodOptions) listPodsFromPrometheus(ctx context.Context) ([]*PodMetrics, error) {
	client := o.prometheusClient()
	if client == nil {
		return nil, runtime.NewUserError("未配置 Prometheus 指标地址（backend.prometheus-endpoint）")
	}

	ctx, cancel := context.WithTimeout(ctx, podListTimeout)
	defer cancel()

	vector, err := client.Query(ctx, buildPodQuery(o.PodName, o.Namespace, o.ClusterName))
	if err != nil {
		return nil, fmt.Errorf("查询 Pod 指标失败: %w", err)
	}

	pods := make([]*PodMetrics, 0, len(vector))
	for _, sample := range vector {
		pod := &PodMetrics{
			PodName:      string(sample.Metric["pod"]),
			Namespace:    string(sample.Metric["namespace"]),
			ClusterName:  string(sample.Metric["cluster"]),
			PodIP:        string(sample.Metric["pod_ip"]),
			HostIP:       string(sample.Metric["host_ip"]),
			Node:         string(sample.Metric["node"]),
			UUID:         string(sample.Metric["uid"]),
			WorkloadType: string(sample.Metric["created_by_kind"]),
		}
		pod.ServiceName = GetWorkloadName(pod.PodName, pod.WorkloadType)
		pods = append(pods, pod)
	}

	if len(pods) > maxMatchedPods {
		return nil, runtime.NewUserError("匹配到 %d 个 Pod，超过上限 %d，请补全 Pod 名称缩小查询范围",
			len(pods), maxMatchedPods)
	}

	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].PodName < pods[j].PodName
	})

	return pods, nil
}

// prometheusClient returns the configured Prometheus client, or nil when no
// endpoint was configured.
func (o *PodOptions) prometheusClient() prometheusQuerier {
	if o.sr == nil || o.sr.Clientset == nil {
		return nil
	}
	client := o.sr.Clientset.Prometheus()
	if client == nil || !client.Configured() {
		return nil
	}
	return client
}

// prometheusQuerier is the slice of the Prometheus client this command uses.
type prometheusQuerier interface {
	Query(ctx context.Context, query string) (model.Vector, error)
}

// buildPodQuery builds the kube_pod_info selector for the given filters. The
// cluster label is `cluster`, which is what the scraped kube-state-metrics
// carries here (not the `eks_cluster_name` the opsassist deployment uses).
func buildPodQuery(podName, namespace, clusterName string) string {
	labels := make([]string, 0, 3)
	if podName != "" {
		labels = append(labels, fmt.Sprintf(`pod=~".*%s.*"`, escapePromQLRegex(podName)))
	}
	if namespace != "" {
		labels = append(labels, fmt.Sprintf(`namespace=~".*%s.*"`, escapePromQLRegex(namespace)))
	}
	if clusterName != "" {
		labels = append(labels, fmt.Sprintf(`cluster=~".*%s.*"`, escapePromQLRegex(clusterName)))
	}
	if len(labels) == 0 {
		return "kube_pod_info"
	}
	return fmt.Sprintf("kube_pod_info{%s}", strings.Join(labels, ","))
}

// escapePromQLRegex escapes a user-supplied value for use inside a PromQL
// double-quoted matcher, so a value containing a quote or a backslash produces
// a valid query instead of a malformed one.
func escapePromQLRegex(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	return strings.ReplaceAll(v, `"`, `\"`)
}

// enrichMetrics fills each pod's metric fields from the kube-state-metrics and
// cAdvisor series. Individual query failures are logged and skipped: a pod
// whose CPU series is missing is still worth showing.
func (o *PodOptions) enrichMetrics(ctx context.Context, pods []*PodMetrics) {
	client := o.prometheusClient()
	if client == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, podMetricsTimeout)
	defer cancel()

	podMap := make(map[string]*PodMetrics, len(pods))
	namespaceSet := make(map[string]struct{}, len(pods))
	podNameSet := make(map[string]struct{}, len(pods))
	for _, pod := range pods {
		podMap[pod.Namespace+"/"+pod.PodName] = pod
		namespaceSet[pod.Namespace] = struct{}{}
		podNameSet[pod.PodName] = struct{}{}
	}

	commonFilter := fmt.Sprintf(`namespace=~"%s", pod=~"%s"`,
		regexAlternation(namespaceSet), regexAlternation(podNameSet))

	queries := map[string]string{
		"phase":          fmt.Sprintf(`kube_pod_status_phase{%s} == 1`, commonFilter),
		"restarts":       fmt.Sprintf(`sum by (namespace, pod) (kube_pod_container_status_restarts_total{%s})`, commonFilter),
		"created":        fmt.Sprintf(`kube_pod_created{%s}`, commonFilter),
		"cpu_usage":      fmt.Sprintf(`sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{%s, container!="", container!="POD"}[5m]))`, commonFilter),
		"mem_usage":      fmt.Sprintf(`sum by (namespace, pod) (container_memory_working_set_bytes{%s, container!="", container!="POD"})`, commonFilter),
		"cpu_req":        fmt.Sprintf(`sum by (namespace, pod) (kube_pod_container_resource_requests{%s, resource="cpu", unit="core"})`, commonFilter),
		"mem_req":        fmt.Sprintf(`sum by (namespace, pod) (kube_pod_container_resource_requests{%s, resource="memory", unit="byte"})`, commonFilter),
		"cpu_lim":        fmt.Sprintf(`sum by (namespace, pod) (kube_pod_container_resource_limits{%s, resource="cpu", unit="core"})`, commonFilter),
		"mem_lim":        fmt.Sprintf(`sum by (namespace, pod) (kube_pod_container_resource_limits{%s, resource="memory", unit="byte"})`, commonFilter),
		"net_rx":         fmt.Sprintf(`sum by (namespace, pod) (rate(container_network_receive_bytes_total{%s}[5m]))`, commonFilter),
		"net_tx":         fmt.Sprintf(`sum by (namespace, pod) (rate(container_network_transmit_bytes_total{%s}[5m]))`, commonFilter),
		"sa":             fmt.Sprintf(`kube_pod_service_account{%s}`, commonFilter),
		"container_info": fmt.Sprintf(`kube_pod_container_info{%s}`, commonFilter),
	}

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for key, query := range queries {
		wg.Add(1)
		go func(metricKey, q string) {
			defer wg.Done()

			vector, err := client.Query(ctx, q)
			if err != nil {
				slog.WarnContext(ctx, "prometheus query for /pod enrichment failed",
					"metric", metricKey, "error", err)
				return
			}

			// Filling touches shared pod values (and appends), so the writes
			// are serialised rather than relying on distinct fields.
			mu.Lock()
			defer mu.Unlock()
			for _, sample := range vector {
				pod, ok := podMap[string(sample.Metric["namespace"])+"/"+string(sample.Metric["pod"])]
				if !ok {
					continue
				}
				fillPodMetric(pod, metricKey, sample)
			}
		}(key, query)
	}
	wg.Wait()

	for _, pod := range pods {
		if pod.Phase == "" {
			pod.Phase = "Unknown"
		}
		if pod.ServiceAccount == "" {
			pod.ServiceAccount = "default"
		}
	}
}

// regexAlternation renders a set of label values as an escaped regex
// alternation, preserving the exact names rather than letting their regex
// metacharacters match something else.
func regexAlternation(values map[string]struct{}) string {
	parts := make([]string, 0, len(values))
	for v := range values {
		parts = append(parts, escapePromQLRegex(v))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

// fillPodMetric sets one metric field on the pod.
func fillPodMetric(pod *PodMetrics, metricKey string, sample *model.Sample) {
	switch metricKey {
	case "phase":
		pod.Phase = string(sample.Metric["phase"])
	case "restarts":
		pod.Restarts = int32(sample.Value)
	case "created":
		pod.CreatedAt = time.Unix(int64(sample.Value), 0).Format(time.RFC3339)
	case "sa":
		pod.ServiceAccount = string(sample.Metric["service_account"])
	case "cpu_usage":
		pod.CPUUsageCores = ptr.To(float64(sample.Value))
	case "mem_usage":
		pod.MemUsageBytes = ptr.To(float64(sample.Value))
	case "cpu_req":
		pod.CPURequestCores = ptr.To(float64(sample.Value))
	case "mem_req":
		pod.MemRequestBytes = ptr.To(float64(sample.Value))
	case "cpu_lim":
		pod.CPULimitCores = ptr.To(float64(sample.Value))
	case "mem_lim":
		pod.MemLimitBytes = ptr.To(float64(sample.Value))
	case "net_rx":
		pod.NetRxBytesPerSec = ptr.To(float64(sample.Value))
	case "net_tx":
		pod.NetTxBytesPerSec = ptr.To(float64(sample.Value))
	case "container_info":
		name := string(sample.Metric["container"])
		if strings.Contains(strings.ToLower(name), "filebeat") {
			return
		}
		pod.Containers = append(pod.Containers, ContainerInfo{
			Name:  name,
			Image: string(sample.Metric["image"]),
		})
	}
}

// formatCPU renders a core count in millicores, which is the unit that keeps an
// idle pod's hundredths of a core visible.
func formatCPU(cores *float64) string {
	if cores == nil {
		return ""
	}
	milli := *cores * 1000
	if milli >= 1000 {
		return fmt.Sprintf("%.2f cores", *cores)
	}
	return fmt.Sprintf("%.1f m", milli)
}

// formatBytes renders a byte count in Mi or Gi.
func formatBytes(b *float64) string {
	if b == nil {
		return ""
	}
	const (
		mi = 1024 * 1024
		gi = 1024 * mi
	)
	if *b >= gi {
		return fmt.Sprintf("%.2f Gi", *b/gi)
	}
	return fmt.Sprintf("%.2f Mi", *b/mi)
}

// formatRate renders a per-second byte rate.
func formatRate(bytesPerSecond *float64) string {
	if bytesPerSecond == nil {
		return ""
	}
	if *bytesPerSecond > 1024*1024 {
		return fmt.Sprintf("%.2f MB/s", *bytesPerSecond/1024/1024)
	}
	return fmt.Sprintf("%.2f KB/s", *bytesPerSecond/1024)
}

// usagePercent renders "used / request" as a percentage, or "" when either
// value is missing or the request is zero.
func usagePercent(used, request *float64) string {
	if used == nil || ptr.From(request) <= 0 {
		return ""
	}
	return fmt.Sprintf("%.1f%%", (*used / *request)*100)
}

// enrichWithCMDB attaches CMDB metadata to each pod. This is the original data
// path of the command, kept intact behind backend.cmdb-enabled.
func (o *PodOptions) enrichWithCMDB(ctx context.Context, pods []*PodMetrics) error {
	if o.sr == nil || o.sr.Clientset == nil {
		return fmt.Errorf("CMDB 客户端未初始化")
	}
	client := o.sr.Clientset.CMDB()
	if client == nil {
		return fmt.Errorf("CMDB 客户端未初始化")
	}

	for _, pod := range pods {
		nodeID, err := client.SearchPodNode(ctx, pod.PodName, pod.ClusterName, pod.Namespace)
		if err != nil {
			return fmt.Errorf("CMDB 查询 Pod 失败: %w", err)
		}

		summary, err := client.GetNodeSummary(ctx, cmdb.ViewEKS, nodeID)
		if err != nil {
			return fmt.Errorf("CMDB 查询 Pod 摘要失败: %w", err)
		}

		networkRes, _ := client.GetNetworkResources(ctx, cmdb.ViewEKS, nodeID)
		pod.CMDB = &PodCMDBInfo{Summary: summary, Network: networkRes}
	}

	return nil
}

// formatPodMarkdown renders the queried pods as markdown. It is the text form
// of the answer, kept for the surfaces that have no card renderer (REST, MCP,
// the agent tool path); the Lark surface renders renderPodCard instead.
func formatPodMarkdown(pods []*PodMetrics) string {
	var sb strings.Builder
	for i, pod := range pods {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}
		fmt.Fprintf(&sb, "## Pod：`%s`\n\n", pod.PodName)
		for _, f := range podKVFields(pod) {
			fmt.Fprintf(&sb, "- **%s**: `%s`\n", f.Key, f.Value)
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// podField is one row of a pod's key-value rendering. The markdown form and the
// card's table are both derived from the same list, so the two can never
// disagree about what a pod's fields are.
type podField struct {
	Key   string
	Value string
}

// podKVFields flattens one pod into ordered key-value rows. Rows that used to be
// nested sub-lines — one per container, one per CMDB attribute — are rows of
// their own: a table has no sub-lines, and a flat list reads the same in both
// renderings.
func podKVFields(pod *PodMetrics) []podField {
	fields := make([]podField, 0, 24)
	add := func(key, value string) {
		if value == "" || value == "--" {
			return
		}
		fields = append(fields, podField{Key: key, Value: value})
	}

	add("Pod", pod.PodName)
	add("集群", pod.ClusterName)
	add("命名空间", pod.Namespace)
	add("Pod IP", pod.PodIP)
	add("节点", formatNodeInfo(pod.Node, pod.HostIP))
	add("状态", pod.Phase)
	add("工作负载类型", pod.WorkloadType)
	add("创建时间", pod.CreatedAt)
	add("重启次数", fmt.Sprintf("%d", pod.Restarts))
	add("服务账号", pod.ServiceAccount)
	add("CPU 使用量", podUsage(pod.CPUUsageCores, pod.CPURequestCores, formatCPU(pod.CPUUsageCores)))
	add("CPU Request", formatCPU(pod.CPURequestCores))
	add("CPU Limit", formatCPU(pod.CPULimitCores))
	add("内存使用量", podUsage(pod.MemUsageBytes, pod.MemRequestBytes, formatBytes(pod.MemUsageBytes)))
	add("内存 Request", formatBytes(pod.MemRequestBytes))
	add("内存 Limit", formatBytes(pod.MemLimitBytes))
	if pod.NetRxBytesPerSec != nil || pod.NetTxBytesPerSec != nil {
		add("网络", fmt.Sprintf("↓ %s / ↑ %s", dashIfEmpty(formatRate(pod.NetRxBytesPerSec)), dashIfEmpty(formatRate(pod.NetTxBytesPerSec))))
	}

	for _, c := range pod.Containers {
		add("容器", fmt.Sprintf("%s — %s", c.Name, c.Image))
	}

	if info := pod.CMDB; info != nil {
		if summary := info.Summary; summary != nil {
			for _, group := range summary.Data {
				for _, attr := range group.Attrs {
					add(group.GroupName+"/"+attr.Label, attr.Value)
				}
			}
		}
		if network := info.Network; network != nil {
			if network.VPC != nil {
				add("VPC", fmt.Sprintf("%s %s (%s)", network.VPC.ID, network.VPC.Name, network.VPC.CIDR))
			}
			for _, s := range network.Subnets {
				add("服务子网", fmt.Sprintf("%s %s (%s)", s.ID, s.CIDR, s.AvailabilityZone))
			}
			for _, sg := range network.SecurityGroups {
				add("服务安全组", fmt.Sprintf("%s %s", sg.ID, sg.Name))
			}
		}
	}

	return fields
}

// podUsage renders "used / request" as a text bar plus the percentage, so a
// glance at the card shows how close a pod is to its request without arithmetic.
// It falls back to the plain value when no percentage is computable — a pod with
// no request set has nothing to fill the bar against.
func podUsage(used, request *float64, rendered string) string {
	percent := usagePercent(used, request)
	if percent == "" || rendered == "" {
		return rendered
	}

	var pct float64
	if _, err := fmt.Sscanf(percent, "%f%%", &pct); err != nil {
		return rendered
	}

	const blocks = 8
	filled := min(max(int(pct/100*blocks), 0), blocks)
	return fmt.Sprintf("%s%s %s · %s",
		strings.Repeat("▰", filled), strings.Repeat("▱", blocks-filled), percent, rendered)
}

// dashIfEmpty renders "" as "--" so a half-filled network row still reads.
func dashIfEmpty(v string) string {
	if v == "" {
		return "--"
	}
	return v
}

// formatNodeInfo renders the node name and IP.
func formatNodeInfo(nodeName, nodeIP string) string {
	if nodeName == "" {
		return ""
	}
	return fmt.Sprintf("%s (%s)", nodeName, dashIfEmpty(nodeIP))
}

// GetWorkloadName extracts the workload (service) name from a pod name.
//
// Ported from the opsassist implementation so the service name shown next to a
// pod matches what the rest of the team's tooling derives.
func GetWorkloadName(podName, workloadType string) string {
	switch strings.ToLower(workloadType) {
	case "replicaset", "deployment":
		lastHyphen := strings.LastIndex(podName, "-")
		if lastHyphen == -1 {
			return podName
		}
		temp := podName[:lastHyphen]
		secondLastHyphen := strings.LastIndex(temp, "-")
		if secondLastHyphen == -1 {
			return temp
		}
		return temp[:secondLastHyphen]

	case "statefulset":
		lastHyphen := strings.LastIndex(podName, "-")
		if lastHyphen == -1 {
			return podName
		}
		if isNumericSuffix(podName[lastHyphen+1:]) {
			return podName[:lastHyphen]
		}
		return podName

	case "daemonset":
		lastHyphen := strings.LastIndex(podName, "-")
		if lastHyphen == -1 {
			return podName
		}
		return podName[:lastHyphen]

	default:
		slog.Warn("GetWorkloadName: unknown workload type, falling back to Deployment heuristic",
			"pod", podName, "workload_type", workloadType)
		lastHyphen := strings.LastIndex(podName, "-")
		if lastHyphen == -1 {
			return podName
		}
		temp := podName[:lastHyphen]
		secondLastHyphen := strings.LastIndex(temp, "-")
		if secondLastHyphen == -1 {
			return temp
		}
		return temp[:secondLastHyphen]
	}
}

// isNumericSuffix returns true if s is a non-empty string of digits.
func isNumericSuffix(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
