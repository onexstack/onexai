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

package authz

import (
	"context"
	"fmt"
)

// ErrAgentAccessDenied is returned by AuthorizeAgentNames when subj is not
// authorized to use one of the requested agentNames. Callers should
// errors.As this to surface a user-facing "no permission" message instead of a
// generic failure.
type ErrAgentAccessDenied struct {
	Subject   Subject
	AgentName string
	Reason    string
}

func (e *ErrAgentAccessDenied) Error() string {
	return fmt.Sprintf("agent %q access denied for subject %s: %s", e.AgentName, e.Subject.PrimaryID(), e.Reason)
}

// AuthorizeAgentNames checks subj against every non-empty name in agentNames
// via coordinator.AuthorizeAgent, returning the first Deny as an
// *ErrAgentAccessDenied. This is Agent-level authorization (PEP #1); call it
// before a Runner is built for agentNames.
func AuthorizeAgentNames(ctx context.Context, coordinator *Coordinator, subj Subject, agentNames []string) error {
	for _, name := range agentNames {
		if name == "" {
			continue
		}
		decision, reason, err := coordinator.AuthorizeAgent(ctx, subj, name)
		if err != nil {
			return fmt.Errorf("authz: agent authorization error: %w", err)
		}
		if decision == DecisionDeny {
			return &ErrAgentAccessDenied{Subject: subj, AgentName: name, Reason: reason}
		}
	}
	return nil
}
