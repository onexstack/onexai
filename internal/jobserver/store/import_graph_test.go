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

package store_test

import (
	"os/exec"
	"strings"
	"testing"
)

const (
	chatPrefix = "github.com/onexstack/onexai/internal/chat"
	jobstore   = chatPrefix + "/store/jobstore"
)

// TestJobStoreDependencyDirection pins the ownership split chosen for the job
// and cronjob stores: chat owns the CRUD implementation, and jobserver consumes
// exactly that package to run jobs asynchronously. A jobserver dependency on
// any other chat package would drag the bot's model and store layer into the
// consumer binary, which is the coupling this layout exists to avoid.
func TestJobStoreDependencyDirection(t *testing.T) {
	deps := goListDeps(t, "./internal/jobserver/...", "../../..")

	found := false
	for _, dep := range deps {
		if dep == jobstore {
			found = true
			continue
		}
		if strings.HasPrefix(dep, chatPrefix+"/") || dep == chatPrefix {
			t.Errorf("jobserver depends on %s; only %s is allowed", dep, jobstore)
		}
	}
	if !found {
		t.Errorf("jobserver does not depend on %s; the shared store is not wired", jobstore)
	}

	// The shared package itself must stay a leaf: it may read the jobserver
	// model, but it must not reach back into any other chat package.
	for _, dep := range goListDeps(t, jobstore, ".") {
		if dep == jobstore {
			continue
		}
		if strings.HasPrefix(dep, chatPrefix+"/") || dep == chatPrefix {
			t.Errorf("%s depends on %s; it must not import its parent tree", jobstore, dep)
		}
	}
}

// goListDeps returns the transitive dependencies of target, resolved from dir.
func goListDeps(t *testing.T, target, dir string) []string {
	t.Helper()

	cmd := exec.Command("go", "list", "-deps", target)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", target, err)
	}
	return strings.Fields(string(out))
}
