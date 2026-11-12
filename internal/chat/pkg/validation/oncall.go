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
	"time"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// ListOncallTicketRequest is the internal ticket-sync query. The comma
// separated spellings are the wire contract; the parsed slices are filled by
// the validator so neither the handler nor the biz layer repeats the split.
type ListOncallTicketRequest = api.ListOncallTicketRequest

// GetOncallTicketRequest is the single-ticket query.
type GetOncallTicketRequest = api.GetOncallTicketRequest

// ValidateListOncallTicketRequest validates and parses the sync query.
func (v *Validator) ValidateListOncallTicketRequest(_ context.Context, rq *ListOncallTicketRequest) error {
	rq.GroupIDs = splitCSV(rq.Groups)
	if len(rq.GroupIDs) == 0 {
		return errno.ErrInvalidArgument.WithMessage("groups is required and must name at least one duty group")
	}

	statuses, err := parseStatuses(rq.Statuses)
	if err != nil {
		return err
	}

	rq.StatusValues = statuses

	if rq.Since != "" {
		parsed, err := time.Parse(time.RFC3339, rq.Since)
		if err != nil {
			return errno.ErrInvalidArgument.WithMessage(
				"since must be RFC 3339 (e.g. 2026-10-10T09:00:00Z), got %q", rq.Since)
		}

		rq.SinceTime = parsed
	}

	return nil
}

// ValidateGetOncallTicketRequest validates and parses the single-ticket query.
func (v *Validator) ValidateGetOncallTicketRequest(_ context.Context, rq *GetOncallTicketRequest) error {
	if strings.TrimSpace(rq.TicketID) == "" {
		return errno.ErrInvalidArgument.WithMessage("ticket id is required")
	}

	statuses, err := parseStatuses(rq.Statuses)
	if err != nil {
		return err
	}

	rq.StatusValues = statuses

	return nil
}

// parseStatuses is the one place the ticket status vocabulary is enforced at
// the REST edge. The business layer keeps the same check as a defensive
// fallback for non-REST callers.
func parseStatuses(raw string) ([]string, error) {
	statuses := splitCSV(raw)
	if len(statuses) == 0 {
		return nil, errno.ErrInvalidArgument.WithMessage("statuses is required and must name at least one ticket status")
	}

	for _, s := range statuses {
		if !model.IsTicketStatus(s) {
			return nil, errno.ErrInvalidArgument.WithMessage(
				"unknown status %q; accepted statuses are %v", s, model.TicketStatuses())
		}
	}

	return statuses, nil
}

// splitCSV reads a comma-separated query value, dropping blank entries.
func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}

	return out
}
