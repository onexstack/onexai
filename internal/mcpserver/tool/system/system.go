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

// Package system registers the "system" namespace: tools that expose
// read-only information about the host process, e.g. the current time and
// environment variables.
package system

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Namespace is the tool namespace this package registers under.
const Namespace = "system"

// TimeNowResult is the output of the "time.now" tool.
type TimeNowResult struct {
	RFC3339 string `json:"rfc3339"`
	Unix    int64  `json:"unix"`
}

// TimeNow returns the current server time.
func TimeNow(_ context.Context, _ json.RawMessage) (any, error) {
	now := time.Now()
	return TimeNowResult{RFC3339: now.Format(time.RFC3339), Unix: now.Unix()}, nil
}

// EnvGetArgs is the input schema for the "env.get" tool.
type EnvGetArgs struct {
	Name string `json:"name"`
}

// EnvGet returns the value of the named environment variable.
func EnvGet(_ context.Context, args json.RawMessage) (any, error) {
	var in EnvGetArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("system_env.get: invalid arguments: %w", err)
	}

	if in.Name == "" {
		return nil, fmt.Errorf("system_env.get: %q argument is required", "name")
	}

	value, ok := os.LookupEnv(in.Name)
	return map[string]any{"name": in.Name, "value": value, "set": ok}, nil
}
