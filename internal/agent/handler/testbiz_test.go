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

package handler

import (
	"github.com/onexstack/onexai/internal/agent/biz"
	"github.com/onexstack/onexai/internal/agent/store"
)

// newTestBiz builds a biz with only the resource store wired. A test that
// exercises another capability builds its own with the dependency it needs.
func newTestBiz(resources store.AIStore) biz.IBiz {
	return biz.NewBiz(nil, nil, nil, nil, resources, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}
