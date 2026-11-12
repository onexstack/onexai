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

package v1

// NoAgent is the one value of ChatCompletionRequest.agent that names no agent.
//
// It is a request-level sentinel, not a row in the agent table: the run uses
// the engine's builtin assistant, which carries no instruction, no MCP servers,
// no skills and no knowledge bases, and the deployment's default model. Callers
// that want a one-shot completion — no tools, no persona — pass it explicitly;
// an **empty** agent is a validation error, so this value is never implied.
//
// The name is reserved. An agent row created with this name would be shadowed
// by the sentinel.
const NoAgent = "default"
