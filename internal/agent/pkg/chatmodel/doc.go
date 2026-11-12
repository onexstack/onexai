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

// Package chatmodel provides a minimal eino model.ToolCallingChatModel backed
// by an OpenAI-compatible /chat/completions endpoint. It exists so the eino/adk
// agent can run without pulling in a provider-specific eino-ext adapter,
// keeping onexai's dependency surface small.
package chatmodel
