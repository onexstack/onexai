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

// GetHistoryRequest names the conversation whose history is read.
type GetHistoryRequest = api.GetHistoryRequest

// ListMemberRequest names the conversation whose members are read.
type ListMemberRequest = api.ListMemberRequest

// GetImageRequest names the image message whose bytes are read.
type GetImageRequest struct {
	MsgID string `uri:"msgID"`
}

// ValidateGetHistoryRequest validates the conversation identifier.
func (v *Validator) ValidateGetHistoryRequest(_ context.Context, rq *GetHistoryRequest) error {
	return requireConvID(rq.ConvID)
}

// ValidateListMemberRequest validates the conversation identifier.
func (v *Validator) ValidateListMemberRequest(_ context.Context, rq *ListMemberRequest) error {
	return requireConvID(rq.ConvID)
}

// ValidateSearchMessagesRequest validates the search window.
func (v *Validator) ValidateSearchMessagesRequest(_ context.Context, rq *api.SearchQuery) error {
	if rq.Limit < 0 {
		return errno.ErrInvalidArgument.WithMessage("limit must be a non-negative integer")
	}

	return nil
}

// ValidateGetImageRequest validates the message identifier.
func (v *Validator) ValidateGetImageRequest(_ context.Context, rq *GetImageRequest) error {
	if strings.TrimSpace(rq.MsgID) == "" {
		return errno.ErrInvalidArgument.WithMessage("msgID is required")
	}

	return nil
}

func requireConvID(convID string) error {
	if strings.TrimSpace(convID) == "" {
		return errno.ErrInvalidArgument.WithMessage("convID is required")
	}

	return nil
}
