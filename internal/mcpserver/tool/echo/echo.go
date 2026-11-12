// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// Package echo registers the "echo" namespace: trivial tools with no
// external dependencies, useful for smoke-testing the MCP server itself.
package echo

import (
	"context"
	"encoding/json"
	"fmt"
)

// Namespace is the tool namespace this package registers under.
const Namespace = "echo"

// PingArgs is the input schema for the "ping" tool.
type PingArgs struct {
	Message string `json:"message"`
}

// Ping returns its input message unchanged, prefixed with "pong: ". It is
// used by tests and examples to verify the server round-trips requests
// correctly without depending on any other subsystem.
func Ping(_ context.Context, args json.RawMessage) (any, error) {
	var in PingArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("echo_ping: invalid arguments: %w", err)
		}
	}

	if in.Message == "" {
		in.Message = "pong"
	}

	return fmt.Sprintf("pong: %s", in.Message), nil
}
