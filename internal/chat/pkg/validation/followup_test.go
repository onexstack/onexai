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
	"time"

	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

func TestOncallTicketRequests(t *testing.T) {
	v := New(nil)
	ctx := context.Background()

	list := &ListOncallTicketRequest{
		Groups:   "g1, g2",
		Statuses: "open, closed",
		Since:    "2026-10-10T09:00:00Z",
	}
	if err := v.ValidateListOncallTicketRequest(ctx, list); err != nil {
		t.Fatalf("ValidateListOncallTicketRequest() error = %v", err)
	}

	if len(list.GroupIDs) != 2 || len(list.StatusValues) != 2 || list.SinceTime.IsZero() {
		t.Errorf("parsed query = %+v", list)
	}

	if got := list.SinceTime.Format(time.RFC3339); got != list.Since {
		t.Errorf("since = %q, want %q", got, list.Since)
	}

	tests := []struct {
		name    string
		rq      *ListOncallTicketRequest
		wantErr bool
	}{
		{"groups required", &ListOncallTicketRequest{Statuses: "open"}, true},
		{"statuses required", &ListOncallTicketRequest{Groups: "g1"}, true},
		{"unknown status", &ListOncallTicketRequest{Groups: "g1", Statuses: "bogus"}, true},
		{"bad since", &ListOncallTicketRequest{Groups: "g1", Statuses: "open", Since: "yesterday"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateListOncallTicketRequest(ctx, tt.rq)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}

	get := &GetOncallTicketRequest{TicketID: "t1", Statuses: "open"}
	if err := v.ValidateGetOncallTicketRequest(ctx, get); err != nil {
		t.Fatalf("ValidateGetOncallTicketRequest() error = %v", err)
	}

	if len(get.StatusValues) != 1 || get.StatusValues[0] != "open" {
		t.Errorf("status values = %v", get.StatusValues)
	}

	if err := v.ValidateGetOncallTicketRequest(ctx, &GetOncallTicketRequest{Statuses: "open"}); err == nil {
		t.Fatal("a missing ticket id was accepted")
	}
}

func TestPeerRequests(t *testing.T) {
	v := New(nil)
	ctx := context.Background()

	claim := &GetPeerBindClaimRequest{}
	if err := v.ValidateGetPeerBindClaimRequest(ctx, claim); err == nil {
		t.Fatal("a missing claim code was accepted")
	}

	triple := &PeerAgentRequest{Backend: "b", Scope: "-", PeerID: "p"}
	triple.Default()
	if err := v.ValidatePeerAgentRequest(ctx, triple); err != nil {
		t.Fatalf("ValidatePeerAgentRequest() error = %v", err)
	}

	if triple.Scope != "" {
		t.Errorf("scope = %q, want empty", triple.Scope)
	}

	put := &PutPeerAgentRequest{
		Backend: "b", Scope: "-", PeerID: "p",
		Canonical: "subject", Agents: []string{"helper"},
	}
	put.Default()
	if err := v.ValidatePutPeerAgentRequest(ctx, put); err != nil {
		t.Fatalf("ValidatePutPeerAgentRequest() error = %v", err)
	}

	if put.Scope != "" {
		t.Errorf("scope = %q, want empty", put.Scope)
	}

	tests := []struct {
		name    string
		rq      *PutPeerAgentRequest
		wantErr bool
	}{
		{"missing backend", &PutPeerAgentRequest{PeerID: "p", Canonical: "c"}, true},
		{"missing canonical", &PutPeerAgentRequest{Backend: "b", PeerID: "p"}, true},
		{"empty agent name", &PutPeerAgentRequest{Backend: "b", PeerID: "p", Canonical: "c", Agents: []string{" "}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidatePutPeerAgentRequest(ctx, tt.rq)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTicketAssetAndCommandRequests(t *testing.T) {
	v := New(nil)
	ctx := context.Background()

	if err := v.ValidateGetTicketRequest(ctx, &GetTicketRequest{}); err == nil {
		t.Fatal("a missing ticket id was accepted")
	}

	if err := v.ValidateListTicketNoteRequest(ctx, &ListTicketNoteRequest{TicketID: "t1"}); err != nil {
		t.Fatalf("ValidateListTicketNoteRequest() error = %v", err)
	}

	if err := v.ValidateCreateAssetRequest(ctx, &agentv1.CreateAssetRequest{}); err == nil {
		t.Fatal("an asset without data was accepted")
	}

	asset := &agentv1.CreateAssetRequest{Data: "aGVsbG8="}
	asset.Default()
	if err := v.ValidateCreateAssetRequest(ctx, asset); err != nil {
		t.Fatalf("ValidateCreateAssetRequest() error = %v", err)
	}

	if asset.GetSource() != "upload" || asset.GetKind() != "image" {
		t.Errorf("asset defaults = %q/%q", asset.GetKind(), asset.GetSource())
	}

	if err := v.ValidateExecCommandRequest(ctx, &v1.ExecCommandRequest{}); err == nil {
		t.Fatal("an empty command was accepted")
	}

	if err := v.ValidateExecCommandRequest(ctx, &v1.ExecCommandRequest{Command: "/askai hi"}); err != nil {
		t.Fatalf("ValidateExecCommandRequest() error = %v", err)
	}
}
