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

	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// The request shapes below are plain structs rather than protobuf messages
// because they exist only at the REST edge: they bind path and query fields
// that the generated messages do not model (or model with no binding tags).
// They live here because the handler and its validator both need the exact same
// shape; a second definition would let the two drift.

// ClearSessionRequest names the conversation whose memory is reset.
type ClearSessionRequest struct {
	SessionID string `uri:"sessionID"`
}

// The request shapes below are aliases of their API-package definitions so the
// handler, the validator and the business layer share one type.

// defaultShareTTLSeconds mirrors the API default; kept here for the validators'
// tests.
const defaultShareTTLSeconds = 7 * 24 * 60 * 60

// ListSessionRunRequest lists the runs of one conversation.
type ListSessionRunRequest = v1.ListSessionRunQuery

// ListApprovalRequest lists pending approvals.
type ListApprovalRequest = v1.ListApprovalQuery

// ListRevisionRequest lists the revisions an agent has executed.
type ListRevisionRequest = v1.ListRevisionQuery

// GetRevisionRequest names one revision.
type GetRevisionRequest = v1.GetRevisionParams

// CreateSessionShareRequest is the input to the share endpoint.
type CreateSessionShareRequest = v1.CreateSessionShareParams

// ListScheduledExecutionRequest lists the executions of one schedule.
type ListScheduledExecutionRequest = v1.ListScheduledExecutionQuery

// GetLogsRequest mirrors the argument names of the external log tool.
type GetLogsRequest = v1.GetLogsParams

// ListK8sEventRequest mirrors the argument names of the external event tool.
type ListK8sEventRequest = v1.ListK8sEventParams

// AgentAccessRequest is the input to the authorization-decision endpoint.
type AgentAccessRequest struct {
	SubjectID string `json:"subjectID"`
	AgentName string `json:"agentName"`
}

// ValidateClearSessionRequest validates the session identifier.
func (v *Validator) ValidateClearSessionRequest(_ context.Context, rq *ClearSessionRequest) error {
	if strings.TrimSpace(rq.SessionID) == "" {
		return errno.ErrInvalidArgument.WithMessage("sessionID is required")
	}

	return nil
}

// ValidateCreateMemoryRequest validates one memory declaration.
func (v *Validator) ValidateCreateMemoryRequest(_ context.Context, rq *v1.CreateMemoryRequest) error {
	if rq.GetMemory() == nil || strings.TrimSpace(rq.GetMemory().GetName()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory name is required")
	}
	return validateMemoryPayload(rq.GetMemory())
}

// ValidateGetMemoryRequest validates a memory lookup.
func (v *Validator) ValidateGetMemoryRequest(_ context.Context, rq *v1.GetMemoryRequest) error {
	if strings.TrimSpace(rq.GetName()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory name is required")
	}
	return nil
}

// ValidateListMemoryRequest validates a memory listing.
func (v *Validator) ValidateListMemoryRequest(_ context.Context, rq *v1.ListMemoryRequest) error {
	if rq.GetOffset() < 0 || rq.GetLimit() < 0 {
		return errno.ErrInvalidArgument.WithMessage("offset and limit must be non-negative")
	}
	return nil
}

// ValidateUpdateMemoryRequest validates a memory update.
func (v *Validator) ValidateUpdateMemoryRequest(_ context.Context, rq *v1.UpdateMemoryRequest) error {
	if rq.GetMemory() == nil || strings.TrimSpace(rq.GetMemory().GetName()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory name is required")
	}
	return validateMemoryPayload(rq.GetMemory())
}

// ValidateDeleteMemoryRequest validates a memory deletion.
func (v *Validator) ValidateDeleteMemoryRequest(_ context.Context, rq *v1.DeleteMemoryRequest) error {
	if strings.TrimSpace(rq.GetName()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory name is required")
	}
	return nil
}

func validateMemoryPayload(m *v1.Memory) error {
	if !m.GetEnabled() {
		return nil
	}
	if strings.TrimSpace(m.GetEmbeddingModel()) == "" {
		return errno.ErrInvalidArgument.WithMessage("an enabled memory needs an embedding model")
	}
	if m.GetEmbeddingDimensions() <= 0 {
		return errno.ErrInvalidArgument.WithMessage("embedding dimensions must be positive")
	}
	if strings.TrimSpace(m.GetEmbeddingVersion()) == "" {
		return errno.ErrInvalidArgument.WithMessage("embedding version is required")
	}
	return nil
}

// ValidateCreateMemoryEntryRequest validates one entry write.
func (v *Validator) ValidateCreateMemoryEntryRequest(_ context.Context, rq *v1.CreateMemoryEntryRequest) error {
	if strings.TrimSpace(rq.GetMemory()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory name is required")
	}
	if rq.GetEntry() == nil || strings.TrimSpace(rq.GetEntry().GetContent()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory content is required")
	}
	if strings.TrimSpace(rq.GetEntry().GetSubject()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory subject is required")
	}
	return nil
}

// ValidateGetMemoryEntryRequest validates one entry lookup.
func (v *Validator) ValidateGetMemoryEntryRequest(_ context.Context, rq *v1.GetMemoryEntryRequest) error {
	if strings.TrimSpace(rq.GetMemory()) == "" || strings.TrimSpace(rq.GetId()) == "" ||
		strings.TrimSpace(rq.GetSubject()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory, id and subject are required")
	}
	return nil
}

// ValidateListMemoryEntriesRequest validates an entry listing.
func (v *Validator) ValidateListMemoryEntriesRequest(_ context.Context, rq *v1.ListMemoryEntriesRequest) error {
	if strings.TrimSpace(rq.GetMemory()) == "" || strings.TrimSpace(rq.GetSubject()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory and subject are required")
	}
	if rq.GetOffset() < 0 || rq.GetLimit() < 0 || rq.GetLimit() > 200 {
		return errno.ErrInvalidArgument.WithMessage("offset must be non-negative and limit must be between 0 and 200")
	}
	return nil
}

// ValidateUpdateMemoryEntryRequest validates one entry update.
func (v *Validator) ValidateUpdateMemoryEntryRequest(_ context.Context, rq *v1.UpdateMemoryEntryRequest) error {
	if strings.TrimSpace(rq.GetMemory()) == "" || strings.TrimSpace(rq.GetId()) == "" ||
		strings.TrimSpace(rq.GetSubject()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory, id and subject are required")
	}
	return nil
}

// ValidateDeleteMemoryEntryRequest validates one entry deletion.
func (v *Validator) ValidateDeleteMemoryEntryRequest(_ context.Context, rq *v1.DeleteMemoryEntryRequest) error {
	if strings.TrimSpace(rq.GetMemory()) == "" || strings.TrimSpace(rq.GetId()) == "" ||
		strings.TrimSpace(rq.GetSubject()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory, id and subject are required")
	}
	return nil
}

// ValidateBatchDeleteMemoryEntriesRequest validates a batch deletion.
func (v *Validator) ValidateBatchDeleteMemoryEntriesRequest(
	_ context.Context, rq *v1.BatchDeleteMemoryEntriesRequest,
) error {
	if strings.TrimSpace(rq.GetMemory()) == "" || strings.TrimSpace(rq.GetSubject()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory and subject are required")
	}
	if len(rq.GetIds()) == 0 || len(rq.GetIds()) > 100 {
		return errno.ErrInvalidArgument.WithMessage("ids must contain between 1 and 100 entries")
	}
	return nil
}

// ValidateSearchMemoryEntriesRequest validates a recall request.
func (v *Validator) ValidateSearchMemoryEntriesRequest(
	_ context.Context, rq *v1.SearchMemoryEntriesRequest,
) error {
	if strings.TrimSpace(rq.GetMemory()) == "" || strings.TrimSpace(rq.GetSubject()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory and subject are required")
	}
	if strings.TrimSpace(rq.GetQuery()) == "" {
		return errno.ErrInvalidArgument.WithMessage("query is required")
	}
	if rq.GetTopK() < 0 || rq.GetTopK() > 100 {
		return errno.ErrInvalidArgument.WithMessage("topK must be between 0 and 100")
	}
	return nil
}

// ValidateGetMemoryStatsRequest validates a stats request.
func (v *Validator) ValidateGetMemoryStatsRequest(_ context.Context, rq *v1.GetMemoryStatsRequest) error {
	if strings.TrimSpace(rq.GetMemory()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory name is required")
	}
	return nil
}

// ValidateListMemorySubjectsRequest validates a subject listing.
func (v *Validator) ValidateListMemorySubjectsRequest(
	_ context.Context, rq *v1.ListMemorySubjectsRequest,
) error {
	if strings.TrimSpace(rq.GetMemory()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory name is required")
	}
	return nil
}

// ValidateListMemoryAuditRequest validates an audit listing.
func (v *Validator) ValidateListMemoryAuditRequest(
	_ context.Context, rq *v1.ListMemoryAuditRequest,
) error {
	if strings.TrimSpace(rq.GetMemory()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory name is required")
	}
	if rq.GetOffset() < 0 || rq.GetLimit() < 0 || rq.GetLimit() > 200 {
		return errno.ErrInvalidArgument.WithMessage("offset must be non-negative and limit must be between 0 and 200")
	}
	return nil
}

// ValidateReembedMemoryRequest validates a re-embedding batch.
func (v *Validator) ValidateReembedMemoryRequest(
	_ context.Context, rq *v1.ReembedMemoryRequest,
) error {
	if strings.TrimSpace(rq.GetMemory()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory name is required")
	}
	if rq.GetLimit() < 0 || rq.GetLimit() > 200 {
		return errno.ErrInvalidArgument.WithMessage("limit must be between 0 and 200")
	}
	return nil
}

// ValidateEvalMemoryRequest validates a retrieval evaluation.
func (v *Validator) ValidateEvalMemoryRequest(_ context.Context, rq *v1.EvalMemoryRequest) error {
	if strings.TrimSpace(rq.GetMemory()) == "" || strings.TrimSpace(rq.GetSubject()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory and subject are required")
	}
	if len(rq.GetCases()) == 0 {
		return errno.ErrInvalidArgument.WithMessage("at least one evaluation case is required")
	}
	for i, item := range rq.GetCases() {
		if strings.TrimSpace(item.GetQuery()) == "" {
			return errno.ErrInvalidArgument.WithMessage("case %d has an empty query", i)
		}
	}
	if rq.GetTopK() < 0 || rq.GetTopK() > 100 {
		return errno.ErrInvalidArgument.WithMessage("topK must be between 0 and 100")
	}
	return nil
}

// ValidatePurgeMemorySubjectRequest validates a subject purge.
func (v *Validator) ValidatePurgeMemorySubjectRequest(
	_ context.Context, rq *v1.PurgeMemorySubjectRequest,
) error {
	if strings.TrimSpace(rq.GetMemory()) == "" || strings.TrimSpace(rq.GetSubject()) == "" {
		return errno.ErrInvalidArgument.WithMessage("memory and subject are required")
	}
	return nil
}

// ValidateListSessionRunRequest validates the conversation and page size.
func (v *Validator) ValidateListSessionRunRequest(_ context.Context, rq *ListSessionRunRequest) error {
	if strings.TrimSpace(rq.ID) == "" {
		return errno.ErrInvalidArgument.WithMessage("session id is required")
	}

	if rq.Limit < 0 {
		return errno.ErrInvalidArgument.WithMessage("limit must be a non-negative integer")
	}

	return nil
}

// ValidateListApprovalRequest validates the page size.
func (v *Validator) ValidateListApprovalRequest(_ context.Context, rq *ListApprovalRequest) error {
	if rq.Limit < 0 {
		return errno.ErrInvalidArgument.WithMessage("limit must be a non-negative integer")
	}

	return nil
}

// ValidateListRevisionRequest validates the page size.
func (v *Validator) ValidateListRevisionRequest(_ context.Context, rq *ListRevisionRequest) error {
	if rq.Limit < 0 {
		return errno.ErrInvalidArgument.WithMessage("limit must be a non-negative integer")
	}

	return nil
}

// ValidateGetRevisionRequest validates the revision identifier.
func (v *Validator) ValidateGetRevisionRequest(_ context.Context, rq *GetRevisionRequest) error {
	if strings.TrimSpace(rq.ID) == "" {
		return errno.ErrInvalidArgument.WithMessage("revision id is required")
	}

	return nil
}

// ValidateCreateSessionShareRequest validates the conversation and lifetime.
func (v *Validator) ValidateCreateSessionShareRequest(_ context.Context, rq *CreateSessionShareRequest) error {
	if strings.TrimSpace(rq.ID) == "" {
		return errno.ErrInvalidArgument.WithMessage("session id is required")
	}

	if rq.TTLSeconds != nil && *rq.TTLSeconds <= 0 {
		return errno.ErrInvalidArgument.WithMessage("ttlSeconds must be a positive integer")
	}

	return nil
}

// ValidateListScheduledExecutionRequest validates the schedule and page size.
func (v *Validator) ValidateListScheduledExecutionRequest(_ context.Context, rq *ListScheduledExecutionRequest) error {
	if strings.TrimSpace(rq.Name) == "" {
		return errno.ErrInvalidArgument.WithMessage("name is required")
	}

	if rq.Limit < 0 {
		return errno.ErrInvalidArgument.WithMessage("limit must be a non-negative integer")
	}

	return nil
}

// ValidateGetLogsRequest validates the arguments the log tool requires.
func (v *Validator) ValidateGetLogsRequest(_ context.Context, rq *GetLogsRequest) error {
	if rq.AccountID == "" || rq.ClusterName == "" || rq.Namespace == "" || rq.PodName == "" {
		return errno.ErrInvalidArgument.WithMessage(
			"account_id, cluster_name, namespace and pod_name are all required")
	}

	return nil
}

// ValidateListK8sEventRequest validates the arguments the event tool requires.
func (v *Validator) ValidateListK8sEventRequest(_ context.Context, rq *ListK8sEventRequest) error {
	if rq.AccountID == "" || rq.ClusterName == "" || rq.Namespace == "" {
		return errno.ErrInvalidArgument.WithMessage(
			"account_id, cluster_name and namespace are all required")
	}

	return nil
}

// ValidateAgentAccessRequest validates the authorization-decision question.
func (v *Validator) ValidateAgentAccessRequest(_ context.Context, rq *AgentAccessRequest) error {
	if strings.TrimSpace(rq.SubjectID) == "" || strings.TrimSpace(rq.AgentName) == "" {
		return errno.ErrInvalidArgument.WithMessage("subjectID and agentName are both required")
	}

	return nil
}

// ValidateGetAssetRequest validates the asset identifier.
func (v *Validator) ValidateGetAssetRequest(_ context.Context, rq *v1.GetAssetRequest) error {
	return requireAssetID(rq.GetAssetId())
}

// ValidateDeleteAssetRequest validates the asset identifier.
func (v *Validator) ValidateDeleteAssetRequest(_ context.Context, rq *v1.DeleteAssetRequest) error {
	return requireAssetID(rq.GetAssetId())
}

func requireAssetID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errno.ErrInvalidArgument.WithMessage("assetID is required")
	}

	return nil
}
