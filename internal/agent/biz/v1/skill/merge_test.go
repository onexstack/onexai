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

package skill

import (
	"testing"

	"github.com/onexstack/onexai/internal/agent/model"
)

func TestMergeSkillUpdateKeepsStoredFields(t *testing.T) {
	existing := &model.SkillM{
		Mode:       "tool",
		Params:     "p",
		Contract:   "c",
		Entrypoint: "e",
		CreatedAt:  42,
	}
	row := &model.SkillM{Name: "sk"}

	mergeSkillUpdate(row, existing)

	if row.Mode != "tool" || row.Params != "p" || row.Contract != "c" ||
		row.Entrypoint != "e" || row.CreatedAt != 42 {
		t.Fatalf("merge did not preserve stored fields: %+v", row)
	}
}

func TestMergeSkillUpdateKeepsRequestValues(t *testing.T) {
	existing := &model.SkillM{
		Mode:       "tool",
		Params:     "stored",
		Contract:   "stored",
		Entrypoint: "stored",
		CreatedAt:  42,
	}
	row := &model.SkillM{
		Name:       "sk",
		Mode:       "guide",
		Params:     "sent",
		Contract:   "sent",
		Entrypoint: "sent",
		CreatedAt:  7,
	}

	mergeSkillUpdate(row, existing)

	if row.Mode != "guide" || row.Params != "sent" || row.Contract != "sent" ||
		row.Entrypoint != "sent" || row.CreatedAt != 7 {
		t.Fatalf("merge overwrote request values: %+v", row)
	}
}
