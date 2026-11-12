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

//go:build !unix

package agentcli

import (
	"os"
	"os/exec"
	"time"
)

// waitDelay bounds how long Wait keeps waiting after the agent has been told to
// stop. It applies on every platform; only the signalling differs.
const waitDelay = 10 * time.Second

// configureProcessGroup is a no-op where process groups are not a thing: there
// is no group to put the agent in, so cancellation falls back to the standard
// library's own single-process kill.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = waitDelay
}

// terminateGroup kills the agent. Without process groups there is nothing to
// signal as a set, so this stops at the process the caller started.
func terminateGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
