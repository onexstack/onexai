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

// Package clientset aggregates typed clients for the oai-chat-apiserver. It follows
// the kubectl clientset pattern: one Interface, per-service typed clients, and
// fake implementations for unit testing.
package clientset

import (
	"sync"

	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/cmdb"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/lark"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/prometheus"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/zadig"
	"github.com/onexstack/onexai/pkg/clientset/agent"
)

// Interface defines the operations for accessing the typed clients.
type Interface interface {
	// L returns the Lark (Feishu) SDK client.
	L() lark.Interface
	// Agent returns the client for oai-agent-apiserver.
	Agent() agent.Interface
	// CMDB returns the client for the CMDB resource-query API.
	CMDB() cmdb.Interface
	// Zadig returns the client for the Zadig DevOps platform.
	Zadig() zadig.Interface
	// Prometheus returns the client for the Prometheus HTTP API that backs the
	// /pod ChatOps command.
	Prometheus() prometheus.Interface
}

// Clientset provides a unified entry point to access the typed clients.
type Clientset struct {
	lark       lark.Interface
	agent      agent.Interface
	cmdb       cmdb.Interface
	zadig      zadig.Interface
	prometheus prometheus.Interface
}

var (
	// CS is the global singleton clientset, mirroring the legacy clientset pattern.
	CS *Clientset
	// once ensures the clientset is initialized exactly once.
	once sync.Once
)

// New creates a Clientset with the provided client interfaces.
func New(lark lark.Interface, agent agent.Interface, cmdb cmdb.Interface, zadigClient zadig.Interface, prometheusClient prometheus.Interface) *Clientset {
	once.Do(func() {
		CS = &Clientset{
			lark:       lark,
			agent:      agent,
			cmdb:       cmdb,
			zadig:      zadigClient,
			prometheus: prometheusClient,
		}
	})
	return CS
}

// L returns the Lark (Feishu) SDK client.
func (c *Clientset) L() lark.Interface {
	return c.lark
}

// Agent returns the oai-agent-apiserver client.
func (c *Clientset) Agent() agent.Interface {
	return c.agent
}

// CMDB returns the CMDB resource-query client.
func (c *Clientset) CMDB() cmdb.Interface {
	return c.cmdb
}

// Zadig returns the Zadig DevOps platform client.
func (c *Clientset) Zadig() zadig.Interface {
	return c.zadig
}

// Prometheus returns the Prometheus HTTP API client.
func (c *Clientset) Prometheus() prometheus.Interface {
	return c.prometheus
}
