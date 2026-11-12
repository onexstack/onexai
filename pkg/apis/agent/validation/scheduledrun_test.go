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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func TestValidateScheduledRun(t *testing.T) {
	path := field.NewPath("spec")
	valid := &agent.ScheduledRunSpec{
		Agent:    agent.Reference{Name: "sre"},
		Schedule: "0 9 * * 1-5",
		TimeZone: "Asia/Shanghai",
		Prompt:   "report the cluster health",
		ExecutionTimeout: &metav1.Duration{
			Duration: 15 * time.Minute,
		},
	}
	assert.Empty(t, ValidateScheduledRun(valid, path))

	badCron := *valid
	badCron.Schedule = "every morning"
	assert.NotEmpty(t, ValidateScheduledRun(&badCron, path))

	badTZ := *valid
	badTZ.TimeZone = "Mars/Olympus"
	assert.NotEmpty(t, ValidateScheduledRun(&badTZ, path))

	noAgent := *valid
	noAgent.Agent.Name = ""
	assert.NotEmpty(t, ValidateScheduledRun(&noAgent, path))
}
