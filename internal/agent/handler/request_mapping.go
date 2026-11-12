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

package handler

import (
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// principalFromProto maps the wire identity onto the internal one. It returns
// nil for an absent actor so "the actor is the subject" stays representable.
func principalFromProto(p *v1.Principal) *agentapi.Principal {
	if p == nil {
		return nil
	}
	return &agentapi.Principal{ID: p.GetId(), Name: p.GetName()}
}

// generationFromProto maps generation options, preserving the tri-state on
// reasoning (nil means "the caller did not say").
func generationFromProto(g *v1.GenerationConfig) agentapi.GenerationConfig {
	if g == nil {
		return agentapi.GenerationConfig{}
	}
	var maxOut *int
	if g.MaxOutputTokens != nil {
		n := int(*g.MaxOutputTokens)
		maxOut = &n
	}
	return agentapi.GenerationConfig{
		Reasoning:       g.Reasoning,
		Temperature:     g.Temperature,
		MaxOutputTokens: maxOut,
		TopP:            g.TopP,
		ProviderOptions: g.ProviderOptions,
	}
}

// executionOptionsFromProto maps the run policy.
func executionOptionsFromProto(o *v1.ExecutionOptions) agentapi.ExecutionOptions {
	if o == nil {
		return agentapi.ExecutionOptions{}
	}
	out := agentapi.ExecutionOptions{
		Approval: agentapi.Approval{
			Channel:  approvalChannelFromProto(o.GetApproval().GetChannel()),
			Fallback: approvalFallbackFromProto(o.GetApproval().GetFallback()),
		},
	}
	if o.Deadline != nil {
		out.Deadline = o.Deadline.AsDuration()
	}
	return out
}

func approvalChannelFromProto(c v1.Approval_Channel) agentapi.ApprovalChannel {
	switch c {
	case v1.Approval_CHANNEL_INTERACTIVE:
		return agentapi.ApprovalChannelInteractive
	case v1.Approval_CHANNEL_NONE:
		return agentapi.ApprovalChannelNone
	default:
		return agentapi.ApprovalChannelUnspecified
	}
}

func approvalFallbackFromProto(f v1.Approval_Fallback) string {
	switch f {
	case v1.Approval_FALLBACK_DENY:
		return agentapi.ApprovalFallbackDeny
	case v1.Approval_FALLBACK_ERROR:
		return agentapi.ApprovalFallbackError
	default:
		return ""
	}
}

func meteringFromProto(m v1.Metering_Mode) agentapi.Metering {
	switch m {
	case v1.Metering_MODE_CALLER:
		return agentapi.MeteringCaller
	case v1.Metering_MODE_RUNTIME:
		return agentapi.MeteringRuntime
	case v1.Metering_MODE_NONE:
		return agentapi.MeteringNone
	default:
		return agentapi.MeteringUnspecified
	}
}
