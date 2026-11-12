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

package agent

import (
	"context"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authn/method"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// enforceAgentAuthn checks that the caller was authenticated by one of the
// Agent's own authentication Webhooks.
//
// It is the second half of the Agent-level override. The edge middleware picks
// the chain before the run exists; this check is what makes the decision hold
// for a merged selection (each Agent keeps its own requirement) and for an
// internal caller that reaches Ask without the HTTP edge.
//
// AK/SK is deliberately exempt: the signature is the platform's service
// credential and the TokenReview protocol cannot express it, so an Agent cannot
// reasonably require a Webhook for machine-to-machine callers.
func enforceAgentAuthn(ctx context.Context, records []*model.AgentM) error {
	authMethod, ok := method.From(ctx)
	if !ok {
		// No method on the context means an internal entry: a resumed run from
		// the dispatcher, or a caller already admitted. The edge sets a method
		// on every HTTP request, so the check below still covers every request
		// that arrives from outside.
		return nil
	}
	for _, agent := range records {
		if agent == nil || len(agent.AuthnWebhooks) == 0 {
			continue
		}
		if authMethod == method.AKSK {
			continue
		}
		webhook := method.WebhookName(authMethod)
		if webhook != "" && containsString(agent.AuthnWebhooks, webhook) {
			continue
		}
		return errno.ErrUnauthenticated.WithMessage(
			"agent %q requires a caller authenticated by one of its authentication webhooks", agent.Name)
	}
	return nil
}

// containsString reports whether list holds name.
func containsString(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}
