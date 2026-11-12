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

// k8sgptEndpointStore is the cluster k8sgpt endpoint table's store. All five
// standard methods are inherited from the generic store.
//
// Update is a full-row Save, so the caller must hand in the row it read (Get,
// then change the writable fields). Saving a payload built from a request body
// alone would carry a zero primary key and be treated as an insert.
type k8sgptEndpointStore struct {
	*genericstore.Store[model.K8SGPTEndpointM]
}

// Ensure k8sgptEndpointStore implements the K8SGPTEndpointStore interface.
var _ agentstore.K8SGPTEndpointStore = (*k8sgptEndpointStore)(nil)

// newK8SGPTEndpointStore creates a k8sgptEndpointStore backed by s.
func newK8SGPTEndpointStore(s *Store) *k8sgptEndpointStore {
	return &k8sgptEndpointStore{
		Store: genericstore.NewStore[model.K8SGPTEndpointM](s, storelogger.NewLogger()),
	}
}
