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

// ModelSchemaOutput holds the column definitions for a CMDB model.
// Primary key information is embedded in each column's Tag field.
type ModelSchemaOutput struct {
	ModelID string          `json:"model_id"`
	Columns json.RawMessage `json:"columns"`
}

// GetModelSchema fetches column definitions for the given model.
// Primary key information is available in each column's tag field (e.g. "primaryKey;autoIncrement:true").
func (c *Client) GetModelSchema(ctx context.Context, modelID string) (*ModelSchemaOutput, error) {
	if modelID == "" {
		return nil, fmt.Errorf("model_id is required")
	}

	resp, err := c.client.R().
		SetContext(ctx).
		Get(fmt.Sprintf("/ext/api/v1/cmdb/models/%s/columns", modelID))
	if err != nil {
		return nil, fmt.Errorf("get columns %s: %w", modelID, err)
	}
	if !resp.IsStatusSuccess() {
		return nil, fmt.Errorf("get columns %s: http %d", modelID, resp.StatusCode())
	}

	return &ModelSchemaOutput{
		ModelID: modelID,
		Columns: json.RawMessage(resp.Bytes()),
	}, nil
}
