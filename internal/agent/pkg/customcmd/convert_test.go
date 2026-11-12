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

package customcmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
)

func TestModelToProtoNormalizesEnabledStatus(t *testing.T) {
	cases := []struct {
		name, status, want string
	}{
		{"legacy enabled spelling", "", model.StatusEnabled},
		{"already enabled", model.StatusEnabled, model.StatusEnabled},
		{"disabled", model.StatusDisabled, model.StatusDisabled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ModelToProto(&model.CustomCommandM{Status: tc.status})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.GetStatus())
		})
	}
}
