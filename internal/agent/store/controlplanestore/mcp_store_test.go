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

package controlplanestore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// The row exposes the last successful discovery so the console can explain each
// tool; a failed probe keeps the previous list, and the row must mirror that.
func TestMCPFromCRProjectsDiscoveredTools(t *testing.T) {
	cr := &agentv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "onex-courses", Namespace: "default"},
	}
	cr.Status.Tools = []agentv1alpha1.MCPTool{
		{Name: "public_list_courses"},
		{Name: "public_get_course", Description: "按 productID 取课程介绍"},
	}

	row := mcpFromCR(cr)

	assert.Equal(t, []model.MCPTool{
		{Name: "public_list_courses"},
		{Name: "public_get_course", Description: "按 productID 取课程介绍"},
	}, row.Tools)
}

// A server that has never published tools reports none rather than an empty
// slice, so JSON omits the field and the console can tell "not discovered" from
// "exposes no tools".
func TestMCPFromCRWithoutDiscovery(t *testing.T) {
	row := mcpFromCR(&agentv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "default"},
	})

	assert.Nil(t, row.Tools)
}
