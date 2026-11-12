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

package chat

// Mock mode values used to force a command's execution outcome for testing.
const (
	MockNone    = ""
	MockSuccess = "success"
	MockFailed  = "failed"
)

// Builtin agent identifiers for the built-in assistant agent.
const (
	BuiltinModuleID  = "__builtin__"
	BuiltinAgentName = "__builtin__"
)

// Backend names identify which class of caller produced a peer identity. They
// carry the same meaning as im.Message.Backend, and the same value is stored in
// chat.peer_agent.backend.
const (
	// BackendLark is the Lark bot's message surface.
	BackendLark = "lark"
	// BackendOpenAPI is the OpenAPI surface. No caller uses it yet; it is named
	// here so the column's domain is written down in one place.
	BackendOpenAPI = "openapi"
)

// Allocator names select which strategy turns resolved candidates into the final
// agent set. They are configuration values, so they are spelled identically in
// the options package, in this registry, and in a deployment's YAML.
const (
	// AllocatorFirst takes the leading candidate.
	AllocatorFirst = "first"
	// AllocatorAll keeps every candidate.
	AllocatorAll = "all"
)

// Allocators lists the shipped strategies in preference order; the first is the
// default a deployment gets when it configures none.
var Allocators = []string{AllocatorFirst, AllocatorAll}

// The platform's built-in agents, as they are named wherever a name is written:
// in agent.agent, in agent.authz_rule's resource_name, and in the configuration
// that points a conversation at one.
//
// # Why they carry the fz- prefix
//
// `default` is a generic word in a service that hosts several products' agents.
// A bare `default` says nothing about whose it is, and the moment a second
// product ships a built-in the two collide with no way to tell them apart. The
// prefix namespaces them the way `onex-` already namespaces the OneX
// integration's own (`onex-u-<userID>`, `onex-x-max-tier`).
//
// Two prefixes, two scopes, and a reader has to be able to tell them apart:
//
//	onex-*  resources the OneX integration created
//	fz-*    the platform's own built-ins, shared by every product it hosts
const (
	// DefaultAgentName is the built-in that holds the ops toolchain (the
	// zke-kubeagent and zadig-mcp servers) for every subject that can reach it.
	//
	// Its authorization rules grant those servers to `["*"]`, so it is NOT an
	// agent any outsider should be pointed at — see hack/authz-rules-seed.sql,
	// whose allow-* rules spell out exactly what it can reach. It is no longer
	// any deployment's default: chat.internal-default names PublicAgentName, and
	// this one is reached only by an explicit `/agent fz-default`.
	DefaultAgentName = "fz-default"

	// PublicAgentName is the built-in assistant an internal (in-tenant) speaker's
	// chat runs when they have no agent of their own.
	//
	// It declares no ops MCP server, so its ["*"] rules grant general tools and
	// nothing else: it is the safe default for a tenant member who has not bound
	// an account, which is why chat.internal-default names it rather than
	// DefaultAgentName.
	PublicAgentName = "fz-public"

	// VisitorAgentName is the built-in assistant an outside speaker's chat runs
	// when they have no agent of their own — the zero-capability fallback whose
	// existence is the point.
	//
	// It is the counterpart of PublicAgentName across the audience split, and it
	// is deliberately the *less* capable of the two: a speaker the platform
	// cannot place is answered by an agent with no knowledge bases, no skills and
	// no MCP servers, so nothing that was sold to somebody can be reached through
	// it. chat.external-default names it. See hack/fz-visitor-seed.sql.
	//
	// Zero capability now includes slash commands, with one deliberate carve-out:
	// /bind-onex is granted to this agent (and to PublicAgentName) by name in
	// hack/onex-bind-command-seed.sql. An unbound speaker is exactly who binding
	// is for, and this is the agent their conversation resolves to, so a grant of
	// nothing here would be a grant of nothing to the one person who needs it.
	VisitorAgentName = "fz-visitor"
)
