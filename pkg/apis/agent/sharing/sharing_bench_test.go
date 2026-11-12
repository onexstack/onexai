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

package sharing

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// largeGrantSet builds 1000 namespaces × 100 targets, the shape the plan asks
// the index to stay fast at. It is shared by the correctness test and the
// benchmark.
func largeGrantSet() []Grant {
	grants := make([]Grant, 0, 1000*100)
	for ns := 0; ns < 1000; ns++ {
		for i := 0; i < 100; i++ {
			grants = append(grants, Grant{
				Namespace:     fmt.Sprintf("ns-%d", ns),
				FromGroup:     GroupName,
				FromKind:      "Agent",
				FromNamespace: "helper-public",
				ToGroup:       GroupName,
				ToKind:        "Skill",
				ToName:        fmt.Sprintf("skill-%d", i),
			})
		}
	}
	return grants
}

func TestGrantIndexLargeSet(t *testing.T) {
	index := NewGrantIndex(largeGrantSet())
	from := Ref{Group: GroupName, Kind: "Agent", Namespace: "helper-public"}
	assert.True(t, index.Allows(from, Ref{Group: GroupName, Kind: "Skill", Namespace: "ns-999", Name: "skill-99"}))
	assert.False(t, index.Allows(from, Ref{Group: GroupName, Kind: "Skill", Namespace: "ns-unknown", Name: "skill-99"}))
	assert.False(t, index.Allows(from, Ref{Group: GroupName, Kind: "Skill", Namespace: "ns-999", Name: "other"}))
}

func BenchmarkGrantIndexAllows1000x100(b *testing.B) {
	index := NewGrantIndex(largeGrantSet())
	from := Ref{Group: GroupName, Kind: "Agent", Namespace: "helper-public"}
	to := Ref{Group: GroupName, Kind: "Skill", Namespace: "ns-999", Name: "skill-99"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !index.Allows(from, to) {
			b.Fatal("grant not found")
		}
	}
}
