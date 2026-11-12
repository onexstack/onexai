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
	"bytes"
	"os"
)

// filePerm is the mode every persisted skill file gets. Skills are read by the
// agent process and, at most, by an operator debugging a checkout; nothing
// needs them writable by anyone else.
const filePerm os.FileMode = 0o644

// writeFileIfChanged writes content to path only when it differs from what is
// already there.
//
// The check is what makes a periodic revalidation cheap and, more importantly,
// quiet: rewriting an identical SKILL.md on every refresh would churn the
// mtime of files an agent may be reading at that moment, and would make the
// on-disk tree look modified when nothing changed.
func writeFileIfChanged(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return nil
	}
	return os.WriteFile(path, content, filePerm)
}
