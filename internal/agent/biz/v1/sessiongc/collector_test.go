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

package sessiongc

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/onexstack/onexai/internal/agent/model"
)

func TestHasActiveState(t *testing.T) {
	assert.False(t, hasActiveState(nil))
	assert.False(t, hasActiveState([]*model.RunM{{State: model.RunStateCompleted}}))
	assert.True(t, hasActiveState([]*model.RunM{{State: model.RunStateRunning}}))
	assert.True(t, hasActiveState([]*model.RunM{{State: model.RunStateInterrupted}}))
}
