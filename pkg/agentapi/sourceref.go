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

package agentapi

import v1 "github.com/onexstack/onexai/pkg/api/agent/v1"

// The conversions between a SourceRef and the contract's AgentKnowledgeSource
// live here, beside the type they serialise, for the reason imageref.go gives:
// a mapping spelled out per call site becomes a field that quietly stops
// travelling when one site is missed.
//
// Only the outbound direction exists. A reference travels from a retrieval to
// whoever renders it and never back in on a request — a caller has no sources
// to send, and the ask request would have nothing to do with them.

// SourceToProto maps a reference onto the contract message.
func SourceToProto(r SourceRef) *v1.AgentKnowledgeSource {
	return &v1.AgentKnowledgeSource{
		DocumentKey: r.DocumentKey,
		Title:       r.Title,
		Url:         r.URL,
	}
}

// SourcesToProto maps a slice of references, returning nil for an empty slice
// so an absent list is absent on the wire rather than an empty one.
func SourcesToProto(refs []SourceRef) []*v1.AgentKnowledgeSource {
	if len(refs) == 0 {
		return nil
	}
	out := make([]*v1.AgentKnowledgeSource, 0, len(refs))
	for _, r := range refs {
		out = append(out, SourceToProto(r))
	}
	return out
}
