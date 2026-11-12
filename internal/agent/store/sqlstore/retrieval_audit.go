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

package sqlstore

import (
	storelogger "github.com/onexstack/onexstack/pkg/logger/slog/store"
	genericstore "github.com/onexstack/onexstack/pkg/store"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// retrievalAuditStore is the retrieval-audit table's store. All five standard
// methods are inherited from the generic store.
//
// Update is a full-row Save, so the caller must hand in the row it read (Get,
// then change the writable fields). Saving a payload built from a request body
// alone would carry a zero primary key and be treated as an insert.
type retrievalAuditStore struct {
	*genericstore.Store[model.RetrievalAuditM]
}

// Ensure retrievalAuditStore implements the RetrievalAuditStore interface.
var _ agentstore.RetrievalAuditStore = (*retrievalAuditStore)(nil)

// newRetrievalAuditStore creates a retrievalAuditStore backed by s.
func newRetrievalAuditStore(s *Store) *retrievalAuditStore {
	return &retrievalAuditStore{
		Store: genericstore.NewStore[model.RetrievalAuditM](s, storelogger.NewLogger()),
	}
}
