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

package controlplanestore

import (
	"context"
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// headersSecretName names the Secret the store writes for a server's HTTP
// headers.
func headersSecretName(serverName string) string { return serverName + "-headers" }

// mcpStore is the MCP servers accessor over the control plane.
type mcpStore struct {
	*Store[model.MCPM]
}

// Ensure mcpStore implements the MCPStore interface.
var _ store.MCPStore = (*mcpStore)(nil)

// NewMCPStore builds the MCP server accessor.
//
// Headers were a JSON column; they are now a Secret the store owns. The row
// shape the runtime consumes still carries one JSON string, so the conversion
// reads the Secret on the way out — the MCP client needs no change.
func NewMCPStore(client *declclient.Client, namespace string) *mcpStore {
	lister := client.MCPServers()
	secrets := newSecretReader(client.Secrets())

	// load reads one server, including the headers Secret it references. The
	// namespace is a parameter rather than the store's configuration because a
	// cross-namespace reference must read the server's own headers.
	load := func(ctx context.Context, ns, name string) (model.MCPM, bool, error) {
		item, err := lister.MCPServers(ns).Get(name)
		if err != nil {
			if isNotFound(err) {
				return model.MCPM{}, false, nil
			}
			return model.MCPM{}, false, err
		}
		row := mcpFromCR(item)
		if item.Spec.HeadersRef != nil {
			headers, err := secrets.all(ctx, item.Namespace, item.Spec.HeadersRef.Name)
			if err != nil {
				return model.MCPM{}, false, err
			}
			row.Headers = headersJSON(headers)
		}
		return row, true, nil
	}

	return &mcpStore{Store: &Store[model.MCPM]{
		Reader: Reader[model.MCPM]{
			Key:   "name",
			KeyOf: func(row model.MCPM) string { return row.Name },
			GetByKey: func(ctx context.Context, name string) (model.MCPM, bool, error) {
				return load(ctx, namespace, name)
			},
			GetByNamespacedKey: load,
			All: func(ctx context.Context) ([]model.MCPM, error) {
				items, err := lister.List(labels.Everything())
				if err != nil {
					return nil, err
				}
				rows := make([]model.MCPM, 0, len(items))
				for _, item := range items {
					row := mcpFromCR(item)
					if item.Spec.HeadersRef != nil {
						headers, err := secrets.all(ctx, item.Namespace, item.Spec.HeadersRef.Name)
						if err != nil {
							return nil, err
						}
						row.Headers = headersJSON(headers)
					}
					rows = append(rows, row)
				}
				return rows, nil
			},
			Column: mcpColumn,
		},
		CreateFunc: func(ctx context.Context, row *model.MCPM) error {
			ns := rowNamespace(row.Namespace, namespace)
			cr := &agentv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: row.Name, Namespace: ns}}
			MCPToCR(row, cr)
			created, err := client.Clientset.AgentV1alpha1().MCPServers(ns).Create(ctx, cr, metav1.CreateOptions{})
			if err != nil {
				return err
			}
			return writeMCPHeaders(ctx, client, created, row)
		},
		UpdateFunc: func(ctx context.Context, row *model.MCPM) error {
			ns, err := resolveNamespace(
				func() ([]*agentv1alpha1.MCPServer, error) { return lister.List(labels.Everything()) },
				row.Name, rowNamespace(row.Namespace, namespace))
			if err != nil {
				return err
			}
			api := client.Clientset.AgentV1alpha1().MCPServers(ns)
			cr, err := api.Get(ctx, row.Name, metav1.GetOptions{}) //nolint:kubelistcheck // must not read a stale cache
			if err != nil {
				return err
			}
			MCPToCR(row, cr)
			updated, err := api.Update(ctx, cr, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			return writeMCPHeaders(ctx, client, updated, row)
		},
		DeleteFunc: func(ctx context.Context, name string) error {
			// Deleting something already gone is success; a retried delete must not
			// surface as an error. An ambiguous name is refused before the call:
			// guessing would delete another tenant's server.
			ns, err := resolveNamespace(
				func() ([]*agentv1alpha1.MCPServer, error) { return lister.List(labels.Everything()) },
				name, writeNamespace(namespace))
			if err != nil {
				return err
			}
			return ignoreNotFound(client.Clientset.AgentV1alpha1().MCPServers(ns).Delete(ctx, name, metav1.DeleteOptions{}))
		},
	}}
}

// writeMCPHeaders publishes the headers the object references. The Secret is
// owned by the server, so deleting the server reclaims it.
func writeMCPHeaders(ctx context.Context, client *declclient.Client, cr *agentv1alpha1.MCPServer, row *model.MCPM) error {
	headers, err := DecodeHeaders(row.Headers)
	if err != nil {
		return err
	}
	if len(headers) == 0 {
		return nil
	}

	data := make(map[string][]byte, len(headers))
	for name, value := range headers {
		data[name] = []byte(value)
	}
	return writeSecret(ctx, client.KubeClient, cr.Namespace, headersSecretName(cr.Name), data, "MCPServer", cr.Name, cr.UID)
}

// mcpFromCR projects the object onto the row the runtime reads. The object has
// no mode field, so the row reports the only supported value rather than an
// empty string the console would have to treat as "unset".
func mcpFromCR(cr *agentv1alpha1.MCPServer) model.MCPM {
	row := model.MCPM{
		Name:            cr.Name,
		DisplayName:     cr.Spec.DisplayName,
		Description:     cr.Spec.Description,
		Status:          statusOf(cr.Spec.Enabled),
		Mode:            model.MCPModeDirect,
		Protocol:        cr.Spec.Protocol,
		URL:             cr.Spec.URL,
		AllowedTools:    model.StringList(cr.Spec.AllowedTools),
		RequireApproval: model.StringList(cr.Spec.RequireApproval),
		Namespace:       cr.Namespace,
		Labels:          labelsFrom(cr),
		CreatedAt:       unixMilli(cr.CreationTimestamp),
		UpdatedAt:       unixMilli(cr.CreationTimestamp),
	}
	if cr.Spec.ClientConfig != nil {
		row.ClientConfigJSON = encodeJSON(cr.Spec.ClientConfig)
		if cr.Spec.ClientConfig.URL != nil {
			row.URL = *cr.Spec.ClientConfig.URL
		}
	}
	if cr.Spec.ResourceMappings != nil {
		row.ResourceMappings = string(cr.Spec.ResourceMappings.Raw)
	}
	// status.tools is the last successful discovery, kept as-is so the console
	// can explain each tool. A failed probe does not erase it (see
	// controlplane.mcpServerStatus), so a transient outage still lists tools.
	if len(cr.Status.Tools) > 0 {
		row.Tools = make([]model.MCPTool, 0, len(cr.Status.Tools))
		for _, tool := range cr.Status.Tools {
			row.Tools = append(row.Tools, model.MCPTool{Name: tool.Name, Description: tool.Description})
		}
	}
	return row
}

func MCPToCR(row *model.MCPM, cr *agentv1alpha1.MCPServer) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	cr.Spec.Protocol = protocolForAPI(row.Protocol)
	cr.Spec.URL = row.URL
	cr.Spec.ClientConfig = nil
	if row.ClientConfigJSON != "" {
		cfg := &agentv1alpha1.ClientConfig{}
		if err := json.Unmarshal([]byte(row.ClientConfigJSON), cfg); err == nil {
			cr.Spec.ClientConfig = cfg
			cr.Spec.URL = ""
		}
	}
	cr.Spec.AllowedTools = []string(row.AllowedTools)
	cr.Spec.RequireApproval = []string(row.RequireApproval)
	cr.Spec.ResourceMappings = nil
	if row.ResourceMappings != "" {
		cr.Spec.ResourceMappings = &runtime.RawExtension{Raw: []byte(row.ResourceMappings)}
	}
	cr.Spec.HeadersRef = nil
	if row.Headers != "" {
		cr.Spec.HeadersRef = &corev1.LocalObjectReference{Name: headersSecretName(row.Name)}
	}
	setLabels(cr, row.Labels)
}

func DecodeHeaders(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil, err
	}
	return headers, nil
}

// protocolForAPI spells the protocol the way the API accepts it.
//
// The column predates the API's vocabulary: it holds "HTTP" and "SSE", while the
// object accepts streamable-http, sse and stdio. The runtime normalizes either
// spelling when it dials, but the API server validates the value — so a row
// written through the admin path would be refused without this.
func protocolForAPI(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "http", "streamable-http", "streamable_http", "streamablehttp":
		return "streamable-http"
	case "sse", "http+sse":
		return "sse"
	case "stdio":
		return "stdio"
	default:
		// Empty or something unknown: leave it as it is, so the API server's
		// message names the value the operator actually wrote.
		return protocol
	}
}

func mcpColumn(row model.MCPM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	case "workspace", "namespace":
		return row.Namespace, true
	case "protocol":
		return row.Protocol, true
	default:
		return "", false
	}
}
