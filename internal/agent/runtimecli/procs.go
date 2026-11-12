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

package runtimecli

import (
	"bytes"
	"os/exec"
	"sync"
	"time"
)

// DefaultStopGrace is how long a CLI gets between the polite signal and the
// kill. Claude writes its session state on SIGINT, so a shorter grace loses the
// very continuation handle the next turn depends on.
const DefaultStopGrace = 2 * time.Second

// DefaultStderrLimit bounds what is kept of a CLI's stderr.
//
// The tail is not kept because the useful part of a CLI failure is at the top
// (the error it reported first); the point of the bound is that a CLI stuck in a
// loop cannot grow this process's memory without limit. Writes always report
// full length so the child never blocks on a full pipe.
const DefaultStderrLimit = 64 << 10

// boundedBuffer keeps at most limit bytes and discards the rest.
type boundedBuffer struct {
	mu    sync.Mutex
	limit int
	buf   bytes.Buffer
}

func newBoundedBuffer(limit int) *boundedBuffer {
	if limit <= 0 {
		limit = DefaultStderrLimit
	}
	return &boundedBuffer{limit: limit}
}

// Write implements io.Writer. It always reports success: a child that blocks
// because its stderr is not being drained is a hang, and losing the tail of a
// log is not.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) <= remaining {
			b.buf.Write(p)
		} else {
			b.buf.Write(p[:remaining])
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// stopProcess ends a CLI and everything it started.
//
// A CLI spawns its own children — shell commands, language servers, MCP
// servers. Killing only the leader leaves them running, holding ports and
// memory, invisible to this service. So the signal goes to the process group,
// politely first and then not.
func stopProcess(cmd *exec.Cmd, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	configureProcessGroup(cmd)
	signalGroup(pid, interruptSignal)
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	// Scheduled rather than waited for: the caller is the executor's cancel
	// path, and it must return the canceled status now rather than in two
	// seconds. A kill aimed at a group that already exited is harmless.
	go func() {
		time.Sleep(grace)
		signalGroup(pid, killSignal)
	}()
}
