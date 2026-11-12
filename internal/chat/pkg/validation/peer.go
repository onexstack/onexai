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

package validation

import (
	"context"
	"strings"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// GetPeerBindClaimRequest names a binding claim.
type GetPeerBindClaimRequest = api.GetPeerBindClaimRequest

// PeerAgentRequest names one peer binding by its path triple.
type PeerAgentRequest = api.PeerAgentRequest

// PutPeerAgentRequest is the binding upsert body plus the path triple.
type PutPeerAgentRequest = api.PutPeerAgentRequest

// ValidateGetPeerBindClaimRequest validates the claim code.
func (v *Validator) ValidateGetPeerBindClaimRequest(_ context.Context, rq *GetPeerBindClaimRequest) error {
	if strings.TrimSpace(rq.Code) == "" {
		return errno.ErrInvalidArgument.WithMessage("code is required")
	}

	return nil
}

// ValidatePeerAgentRequest validates a peer binding path triple.
func (v *Validator) ValidatePeerAgentRequest(_ context.Context, rq *PeerAgentRequest) error {
	return requirePeerTriple(rq.Backend, rq.PeerID)
}

// ValidatePutPeerAgentRequest validates a peer binding upsert.
func (v *Validator) ValidatePutPeerAgentRequest(_ context.Context, rq *PutPeerAgentRequest) error {
	if err := requirePeerTriple(rq.Backend, rq.PeerID); err != nil {
		return err
	}

	if strings.TrimSpace(rq.Canonical) == "" {
		return errno.ErrInvalidArgument.WithMessage(
			"canonical is required: a binding with no subject would authorize as nobody")
	}

	for _, agent := range rq.Agents {
		if strings.TrimSpace(agent) == "" {
			return errno.ErrInvalidArgument.WithMessage("agents must not contain an empty name")
		}
	}

	return nil
}

func requirePeerTriple(backend, peerID string) error {
	if strings.TrimSpace(backend) == "" || strings.TrimSpace(peerID) == "" {
		return errno.ErrInvalidArgument.WithMessage("backend and peerID are required")
	}

	return nil
}
