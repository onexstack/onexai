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

package agentcli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	// waitDelay bounds how long Wait keeps waiting after the agent has been told
	// to stop, so a child that ignores SIGTERM cannot wedge the caller.
	waitDelay = 10 * time.Second
	// closeGrace is how long terminateGroup gives the group to shut down
	// cleanly before it stops asking.
	closeGrace = 3 * time.Second
	// pollInterval is how often terminateGroup checks whether the group is gone.
	pollInterval = 50 * time.Millisecond
)

// configureProcessGroup puts the agent in its own process group and makes
// cancellation signal the group rather than the process.
//
// This is not tidiness. The agent spawns tools, and tools spawn children of
// their own; signalling only the agent leaves those behind, holding the
// workspace and — for a long-running command — writing to the same files the
// next session will use.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
	cmd.Cancel = func() error { return signalGroup(cmd.Process, syscall.SIGTERM) }
}

// terminateGroup asks the group to stop, then insists.
func terminateGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := signalGroup(p, syscall.SIGTERM); err != nil {
		return err
	}

	deadline := time.Now().Add(closeGrace)
	for time.Now().Before(deadline) {
		if !groupAlive(p) {
			return nil
		}
		time.Sleep(pollInterval)
	}
	return signalGroup(p, syscall.SIGKILL)
}

// signalGroup sends sig to the process group p leads. A negative pid is the
// group, which is what Setpgid above created.
func signalGroup(p *os.Process, sig syscall.Signal) error {
	if p == nil {
		return nil
	}
	// ESRCH means it is already gone, which is the outcome being asked for.
	if err := syscall.Kill(-p.Pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// groupAlive reports whether the group still has a process in it.
func groupAlive(p *os.Process) bool {
	return syscall.Kill(-p.Pid, 0) == nil
}
