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

// a2aInvocationStore is the invocation table's store. All methods are inherited
// from the generic store; only the table differs.
type a2aInvocationStore struct {
	*genericstore.Store[model.A2AInvocationM]
}

var _ agentstore.A2AInvocationStore = (*a2aInvocationStore)(nil)

func newA2AInvocationStore(s *Store) *a2aInvocationStore {
	return &a2aInvocationStore{
		Store: genericstore.NewStore[model.A2AInvocationM](s, storelogger.NewLogger()),
	}
}
