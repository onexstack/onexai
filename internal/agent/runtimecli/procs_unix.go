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

//go:build unix

package runtimecli

import (
	"os/exec"
	"syscall"
)

const (
	interruptSignal = syscall.SIGINT
	killSignal      = syscall.SIGKILL
)

// configureProcessGroup puts the CLI in its own process group, so a signal to
// the group reaches everything it spawned.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalGroup signals the whole group. The negative pid is the POSIX spelling
// for "the process group", and it is the difference between stopping a CLI and
// stopping the tree it started.
func signalGroup(pid int, signal syscall.Signal) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, signal)
}
