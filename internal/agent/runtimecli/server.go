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

package runtimecli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/onexstack/onexai/internal/agent/pkg/a2awire"
)

// Serve runs the A2A surface until ctx is canceled.
func Serve(ctx context.Context, config *Config, addr, publicURL string) error {
	executor := NewExecutor(config)
	handler := a2asrv.NewHandler(executor)

	if publicURL == "" {
		publicURL = "http://127.0.0.1" + normalizedPort(addr)
	}
	card := &a2atype.AgentCard{
		Name:        config.Name,
		Description: config.Description,
		Version:     config.Version,
		SupportedInterfaces: []*a2atype.AgentInterface{
			a2atype.NewAgentInterface(publicURL, a2atype.TransportProtocolJSONRPC),
			a2atype.NewAgentInterface(strings.TrimSuffix(publicURL, "/")+"/rest", a2atype.TransportProtocolHTTPJSON),
		},
		Capabilities: a2atype.AgentCapabilities{
			Streaming: true,
			// Usage is the one extension this surface honours: the CLI reports
			// its counters on the result envelope. HITL is deliberately absent —
			// a one-shot child process cannot hold a pause open across a
			// restart, and advertising a pause this runtime cannot keep would
			// turn a refusal into a lost run.
			Extensions: []a2atype.AgentExtension{{
				URI:         a2awire.UsageExtensionURI,
				Description: "Task-lifetime token usage on the terminal status.",
			}},
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             []a2atype.AgentSkill{},
	}

	mux := http.NewServeMux()
	mux.Handle("/.well-known/agent-card.json", a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rest/", http.StripPrefix("/rest", a2asrv.NewRESTHandler(handler)))
	mux.Handle("/", a2asrv.NewJSONRPCHandler(handler))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	slog.Info("runtimecli: A2A surface listening", "addr", addr, "command", config.Command)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("runtime: %w", err)
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// normalizedPort turns ":8080" into ":8080" and "127.0.0.1:8080" into
// ":8080" so the card URL always has a host.
func normalizedPort(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return ":" + port
	}
	if strings.HasPrefix(addr, ":") {
		return addr
	}
	return ":8080"
}
