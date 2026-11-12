{{/*
_helpers.tpl —— OneXAI Chart 的共用机制。风格与 onex chart 的 _helpers.tpl 一致：
需要服务上下文的助手统一接收 `(dict "root" $ "svc" <服务名>)`，返回 YAML 片段，
调用方用 nindent 定位。所有助手都不含 oai-* 字面量——差异只来自 values。
*/}}

{{/* Chart 名-版本，用于 helm.sh/chart 标签。 */}}
{{- define "onexai.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "onexai.labels" -}}
helm.sh/chart: {{ include "onexai.chart" . }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: onexai
{{- end -}}

{{/* 已启用服务的名字，按字典序排序，保证渲染输出稳定。 */}}
{{- define "onexai.serviceNames" -}}
{{- $names := list -}}
{{- range $n, $s := .Values.services -}}
{{- if $s.enabled -}}
{{- $names = append $names $n -}}
{{- end -}}
{{- end -}}
{{- join " " ($names | sortAlpha) -}}
{{- end -}}

{{/* 镜像引用。仓库名恒等于服务名。 */}}
{{- define "onexai.image" -}}
{{- $g := .root.Values.global.image -}}
{{- printf "%s/%s:%s" $g.registry .svcName (default $g.tag (default (dict) .svcImage).tag) -}}
{{- end -}}

{{/* 某服务的“有效值” = mergeOverwrite(deepCopy global, services.<svc>)。 */}}
{{- define "onexai.effective" -}}
{{- $g := deepCopy .root.Values.global -}}
{{- $s := default (dict) (index .root.Values.services .svc) -}}
{{- toYaml (mergeOverwrite $g $s) -}}
{{- end -}}

{{/*
一个服务的配置文件 Secret。
这是配置文件正文的**唯一**来源：secret-config.yaml 用它渲染，Deployment 用它的
sha256 做 checksum/config 注解——于是某个服务配置变化只滚动它自己的 Pod。

用 Secret 而不是 ConfigMap 的理由见 Chart.yaml 的 description：这几份配置里嵌着口令
与 key，且 chat.internal-credentials 是一个**列表内**的凭据——viper 的 AutomaticEnv
覆盖不了列表项，所以「配置里留空 + 环境变量注入」这条 onex 那边的路子在这里走不通。
*/}}
{{- define "onexai.configSecret" -}}
{{- $root := .root -}}
{{- $svc := .svc -}}
{{- $file := printf "files/%s.yaml" $svc -}}
{{- $eff := fromYaml (include "onexai.effective" (dict "root" $root "svc" $svc)) -}}
{{- $body := tpl (required (printf "onexai: 缺少 %s（运行 ../installation/sync-configs.rb 生成）" $file) ($root.Files.Get $file)) (dict "Values" $eff) -}}
apiVersion: v1
kind: Secret
metadata:
  name: {{ $svc }}
  labels:
    app: {{ $svc }}
    {{- include "onexai.labels" $root | nindent 4 }}
type: Opaque
stringData:
  {{ $svc }}.yaml: |
{{ $body | indent 4 }}
{{- end -}}

{{/*
启动命令。两种 kind：
  apiserver —— /app/<svc> --config=/app/etc/<svc>.yaml
  mcp       —— /app/onexai mcp --mcp-transport streamable-http --config=/app/etc/onexai.yaml
*/}}
{{- define "onexai.command" -}}
{{- $svc := .svc -}}
{{- if eq .kind "mcp" -}}
- /app/onexai
- mcp
- --mcp-transport
- streamable-http
- --config=/app/etc/{{ $svc }}.yaml
{{- else -}}
- /app/{{ $svc }}
- --config=/app/etc/{{ $svc }}.yaml
{{- end -}}
{{- end -}}

{{/*
容器的 env。刻意很短：
  * HOME —— 三个服务都会调 os.UserHomeDir()，distroless 默认不设 HOME。
  * ONEXAI_MCP_TOKEN —— 只有 MCP 服务需要；网络 transport 下没有它拒绝启动。
配置本身走挂载的 Secret，不走 env（见上面 configSecret 的说明）。
*/}}
{{- define "onexai.env" -}}
{{- $root := .root -}}
{{- $svc := .svc -}}
{{- $s := index $root.Values.services $svc -}}
{{- $list := list (dict "name" "HOME" "value" "/data") -}}
{{- if $s.mcpToken -}}
{{- $list = append $list (dict "name" "ONEXAI_MCP_TOKEN" "valueFrom" (dict "secretKeyRef" (dict "name" $root.Values.global.secrets.name "key" "ONEXAI_MCP_TOKEN" "optional" true))) -}}
{{- end -}}
{{- if $s.extraEnv -}}
{{- $list = concat $list $s.extraEnv -}}
{{- end -}}
{{- toYaml $list -}}
{{- end -}}

{{/* 探针。默认：apiserver 打 /healthz；mcp 只能 tcpSocket（它没有 /healthz）。 */}}
{{- define "onexai.probes" -}}
{{- $s := index .root.Values.services .svc -}}
{{- if $s.probes -}}
{{- toYaml $s.probes -}}
{{- else if eq $s.kind "mcp" -}}
readinessProbe:
  tcpSocket: { port: http }
  initialDelaySeconds: 5
  periodSeconds: 10
  timeoutSeconds: 2
  failureThreshold: 3
livenessProbe:
  tcpSocket: { port: http }
  initialDelaySeconds: 15
  periodSeconds: 20
  timeoutSeconds: 2
  failureThreshold: 3
{{- else -}}
readinessProbe:
  httpGet: { path: /healthz, port: http }
  initialDelaySeconds: 5
  periodSeconds: 10
  timeoutSeconds: 2
  failureThreshold: 3
livenessProbe:
  httpGet: { path: /healthz, port: http }
  initialDelaySeconds: 15
  periodSeconds: 20
  timeoutSeconds: 2
  failureThreshold: 3
{{- end -}}
{{- end -}}

{{/*
控制面机密的指纹。declserver 与运行时的 Pod 用同一个值做 checksum 注解，
于是「换了证书/token」只会滚动这几个 Pod，而不是整套服务。
*/}}
{{- define "onexai.controlplaneSecrets" -}}
{{- toYaml .Values.controlplane.secrets | sha256sum -}}
{{- end -}}

{{/*
oai-controller-manager 的配置文件正文。

单独抽成 define 的原因：ConfigMap 用它的正文，Deployment 用它的 sha256 做
checksum/config。若 Deployment 直接 include 自己所在的模板文件，会无限递归。
*/}}
{{- define "onexai.controllerManagerConfig" -}}
{{- $name := .name -}}
{{- $modelKeyEncryptionKey := .modelKeyEncryptionKey -}}
apiVersion: onexcontrollermanager.config.onexai.io/v1beta1
kind: OneXControllerManagerConfiguration
generic:
  # 单副本，没有需要争抢的 leadership；打开只会多一个 Lease 对象。
  leaderElection:
    leaderElect: false
    leaseDuration: 15s
    renewDeadline: 10s
    resourceLock: leases
    resourceName: {{ $name }}
    resourceNamespace: default
    retryPeriod: 2s
  # 探针要够得着，所以绑 0.0.0.0；这只在 Pod 网络内可达。
  bindAddress: 0.0.0.0
  healthzBindAddress: 0.0.0.0:20250
  metricsBindAddress: 0.0.0.0:20251
  controllers:
    - '*'
  parallelism: 16
  syncPeriod: 10h0m0s
garbageCollectorController:
  enableGarbageCollector: true
  concurrentGCSyncs: 20
  gcIgnoredResources:
    - resource: events
    - group: events.k8s.io
      resource: events
# 控制面 → PostgreSQL 的投影。这里关掉：读路径本来就走控制面缓存，投影只是
# 「List 从 O(N) 变 O(page)」的可选优化，而它需要一份 coredb 凭据，没必要在
# 这一层再放一把数据库钥匙。
#
# schema 用 readmodel.DefaultSchema，与 agent/chat 读侧同一个拼写；写侧用同一
# 条规则校验（^[a-z_][a-z0-9_]*$），下划线合法、连字符不合法。
mirror:
  enabled: false
  schema: mirror
# Prompt 聚合：把一个 agent 绑定的多个 Prompt 合成为一个派生 Prompt。
# 单个 Prompt 不需要模型；多于一个时才调 modelRef 指向的模型。
promptAggregation:
  model: deepseek-secured
  maxTokens: 4000
  maxInputTokens: 60000
  timeout: 60s
  # 必须与 oai-agent-apiserver 的 model.api-key-encryption-key 相同；控制面
  # 用用户模型聚合提示词时，用它解开 Secret 里的 enc:v1:。
  modelAPIKeyEncryptionKey: "{{ $modelKeyEncryptionKey }}"
{{- end -}}
