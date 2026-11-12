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

package cmdb

import (
	"context"
	"encoding/json"
	"fmt"
)

// QueryCondition represents an AND/OR filter tree for complex queries.
type QueryCondition struct {
	And []QueryCondition `json:"and,omitempty"`
	Or  []QueryCondition `json:"or,omitempty"`
}

// OrderBy specifies a sort field and direction.
type OrderBy struct {
	Field string `json:"field"`
	Desc  bool   `json:"desc,omitempty"`
}

// SearchModelRequest maps to service.SearchRequest.
// Use Query for simple equality filters; use Conditions for AND/OR combinations.
type SearchModelRequest struct {
	Query         map[string]any    `json:"query,omitempty"`
	Conditions    *QueryCondition   `json:"conditions,omitempty"`
	Fields        []string          `json:"fields,omitempty"`
	OrderBy       []OrderBy         `json:"order_by,omitempty"`
	Page          int               `json:"page"`
	PageSize      int               `json:"page_size"`
	KeyMap        map[string]string `json:"key_map,omitempty"`
	RelatedTables []string          `json:"related_tables,omitempty"`
}

// SearchModelResponse holds raw records whose shape varies by model_id.
type SearchModelResponse struct {
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Records  []map[string]any `json:"records"`
	// SensitiveFiltered reports whether the returned Records were filtered because
	// at least one record belonged to a sensitive AWS account. When true the
	// caller (especially an LLM agent) must tell the user that the query result
	// involved sensitive account data and was filtered.
	SensitiveFiltered bool `json:"sensitive_filtered"`
}

// searchModelAPIResponse is the envelope returned by the CMDB API.
type searchModelAPIResponse struct {
	Success   bool            `json:"success"`
	RawData   json.RawMessage `json:"data"`
	Error     *apiError       `json:"error,omitempty"`
	Timestamp string          `json:"timestamp"`
}

// paginatedData is the paginated object shape of the data field.
type paginatedData struct {
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Records  []map[string]any `json:"records"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// parseSearchModelData handles both response shapes for the data field.
func parseSearchModelData(modelID string, raw json.RawMessage, page, pageSize int) (*SearchModelResponse, error) {
	if len(raw) == 0 {
		return &SearchModelResponse{Page: page, PageSize: pageSize}, nil
	}

	// Try paginated object first.
	if raw[0] == '{' {
		var p paginatedData
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("cmdb search model %s: parse paginated data: %w", modelID, err)
		}
		return &SearchModelResponse{
			Total:    p.Total,
			Page:     p.Page,
			PageSize: p.PageSize,
			Records:  p.Records,
		}, nil
	}

	// Plain array shape.
	if raw[0] == '[' {
		var records []map[string]any
		if err := json.Unmarshal(raw, &records); err != nil {
			return nil, fmt.Errorf("cmdb search model %s: parse array data: %w", modelID, err)
		}
		return &SearchModelResponse{
			Total:    len(records),
			Page:     page,
			PageSize: pageSize,
			Records:  records,
		}, nil
	}

	return nil, fmt.Errorf("cmdb search model %s: unexpected data format", modelID)
}

// SearchModel queries records for any CMDB model and returns raw key-value records.
func (c *Client) SearchModel(ctx context.Context, modelID string, req SearchModelRequest) (*SearchModelResponse, error) {
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 20
	}

	resp, err := c.client.R().
		SetContext(ctx).
		SetBody(req).
		Post(fmt.Sprintf("/ext/api/v1/cmdb/models/%s/records/search", modelID))

	if err != nil {
		return nil, fmt.Errorf("cmdb search model %s: %w", modelID, err)
	}

	var result searchModelAPIResponse
	if err := json.Unmarshal(resp.Bytes(), &result); err != nil {
		if !resp.IsStatusSuccess() {
			return nil, fmt.Errorf("cmdb search model %s: http %d", modelID, resp.StatusCode())
		}
		return nil, fmt.Errorf("cmdb search model %s: parse response: %w", modelID, err)
	}
	if !result.Success {
		message := "unknown error"
		if result.Error != nil {
			message = result.Error.Message
		}
		return nil, fmt.Errorf("cmdb search model %s: %s", modelID, message)
	}

	result2, parseErr := parseSearchModelData(modelID, result.RawData, req.Page, req.PageSize)
	if parseErr != nil {
		return nil, parseErr
	}

	if len(req.Fields) > 0 {
		fieldSet := make(map[string]struct{}, len(req.Fields))
		for _, f := range req.Fields {
			fieldSet[f] = struct{}{}
		}
		for _, rec := range result2.Records {
			for k := range rec {
				if _, ok := fieldSet[k]; !ok {
					delete(rec, k)
				}
			}
		}
	}

	return result2, nil
}
