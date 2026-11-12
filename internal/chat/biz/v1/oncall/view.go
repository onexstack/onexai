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

package oncall

import (
	"context"

	"github.com/onexstack/onexai/internal/chat/api"
	chatknown "github.com/onexstack/onexai/pkg/known/chat"
)

// ListForSyncView is the REST wire shape of ListForSync.
//
// backend and appID travel as top-level facts about this *service* rather than
// as fields on each ticket: they are what the bot is, not what a ticket is. A
// consumer cannot know them and must not guess — a hardcoded copy would be one
// deployment away from resolving nobody.
func (b *oncallBiz) ListForSyncView(
	ctx context.Context, req *api.ListOncallTicketRequest, appID string,
) (*api.ListOncallTicketResponse, error) {
	candidates, err := b.ListForSync(ctx, SyncQuery{
		GroupIDs: req.GroupIDs,
		Statuses: req.StatusValues,
		Since:    req.SinceTime,
		Limit:    req.Limit,
	})
	if err != nil {
		return nil, err
	}

	items := make([]*api.OncallCandidate, 0, len(candidates))
	for _, cand := range candidates {
		items = append(items, &api.OncallCandidate{TicketM: cand.Ticket})
	}
	return &api.ListOncallTicketResponse{
		Backend:    chatknown.BackendLark,
		AppID:      appID,
		Groups:     req.GroupIDs,
		Statuses:   req.StatusValues,
		Total:      len(items),
		Candidates: items,
	}, nil
}

// TicketView is the REST wire shape of Ticket.
func (b *oncallBiz) TicketView(
	ctx context.Context, req *api.GetOncallTicketRequest, appID string,
) (*api.GetOncallTicketResponse, error) {
	cand, err := b.Ticket(ctx, req.TicketID, req.StatusValues)
	if err != nil {
		return nil, err
	}
	return &api.GetOncallTicketResponse{
		Backend: chatknown.BackendLark,
		AppID:   appID,
		Ticket:  &api.OncallCandidate{TicketM: cand.Ticket, Eligible: cand.Eligible},
	}, nil
}
