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

// Command oai-agent-runtime runs a command-line agent (Claude Code, Codex, or
// any tool with a text/stream-json interface) as an A2A runtime. The gateway
// reaches it through RuntimeProfile.endpoint; the CLI's own configuration stays
// in the image or in the hosting environment.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/onexstack/onexai/internal/agent/runtimecli"
)

func main() {
	var (
		configPath = flag.String("config", "/config/runtime.json", "Path to the runtime config JSON")
		addr       = flag.String("addr", ":8080", "A2A listen address")
		publicURL  = flag.String("public-url", "", "Base URL advertised in the Agent Card; empty derives it from --addr")
		logLevel   = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	)
	flag.Parse()

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	config, err := runtimecli.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runtimecli.Serve(ctx, config, *addr, *publicURL); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
