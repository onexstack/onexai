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

// GetTicketRequest names one ticket.
type GetTicketRequest = api.GetTicketRequest

// ListTicketNoteRequest names the ticket whose notes are read.
type ListTicketNoteRequest = api.ListTicketNoteRequest

// ValidateListTicketRequest validates the page window.
func (v *Validator) ValidateListTicketRequest(_ context.Context, rq *api.ListTicketQuery) error {
	if rq.Page < 0 {
		return errno.ErrInvalidArgument.WithMessage("page must be a non-negative integer")
	}

	if rq.PageSize < 0 {
		return errno.ErrInvalidArgument.WithMessage("page_size must be a non-negative integer")
	}

	return nil
}

// ValidateGetTicketRequest validates the ticket identifier.
func (v *Validator) ValidateGetTicketRequest(_ context.Context, rq *GetTicketRequest) error {
	return requireTicketID(rq.TicketID)
}

// ValidateListTicketNoteRequest validates the ticket identifier.
func (v *Validator) ValidateListTicketNoteRequest(_ context.Context, rq *ListTicketNoteRequest) error {
	return requireTicketID(rq.TicketID)
}

func requireTicketID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errno.ErrInvalidArgument.WithMessage("ticket id is required")
	}

	return nil
}
