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
	"testing"

	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

func TestChatRequestRules(t *testing.T) {
	v := New(nil)
	ctx := context.Background()

	tests := []struct {
		name    string
		run     func() error
		wantErr bool
	}{
		{
			name: "get on-call group without a name",
			run: func() error {
				return v.ValidateGetOncallGroupRequest(ctx, &GetOncallGroupRequest{})
			},
			wantErr: true,
		},
		{
			name: "delete on-call group with a name",
			run: func() error {
				return v.ValidateDeleteOncallGroupRequest(ctx, &DeleteOncallGroupRequest{Name: "sre"})
			},
		},
		{
			name: "exec named command without a name",
			run: func() error {
				return v.ValidateExecNamedCommandRequest(ctx, &ExecNamedCommandRequest{})
			},
			wantErr: true,
		},
		{
			name: "list commands with a negative limit",
			run: func() error {
				return v.ValidateListCommandRequest(ctx, &v1.ListCommandRequest{Limit: -1})
			},
			wantErr: true,
		},
		{
			name: "list commands with a non-negative window",
			run: func() error {
				return v.ValidateListCommandRequest(ctx, &v1.ListCommandRequest{Offset: 0, Limit: 20})
			},
		},
		{
			name: "history without a conversation id",
			run: func() error {
				return v.ValidateGetHistoryRequest(ctx, &GetHistoryRequest{})
			},
			wantErr: true,
		},
		{
			name: "members with a conversation id",
			run: func() error {
				return v.ValidateListMemberRequest(ctx, &ListMemberRequest{ConvID: "conv-1"})
			},
		},
		{
			name: "image without a message id",
			run: func() error {
				return v.ValidateGetImageRequest(ctx, &GetImageRequest{})
			},
			wantErr: true,
		},
		{
			name: "asset content without an asset id",
			run: func() error {
				return v.ValidateGetAssetContentRequest(ctx, &GetAssetContentRequest{})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
