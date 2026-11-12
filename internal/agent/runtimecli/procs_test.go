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
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBoundedBufferTruncatesWithoutBlocking pins both halves of the contract:
// the child always sees a successful write (so it never blocks on a full pipe)
// and the retained text never exceeds the limit (so a looping CLI cannot grow
// this process).
func TestBoundedBufferTruncatesWithoutBlocking(t *testing.T) {
	buffer := newBoundedBuffer(16)

	written, err := buffer.Write([]byte(strings.Repeat("a", 10)))
	require.NoError(t, err)
	assert.Equal(t, 10, written)

	written, err = buffer.Write([]byte(strings.Repeat("b", 100)))
	require.NoError(t, err)
	assert.Equal(t, 100, written, "the full length is reported so the child does not block")

	assert.Len(t, buffer.String(), 16)
	assert.True(t, strings.HasPrefix(buffer.String(), "aaaaaaaaaa"))
}

// TestCancelStopsTheWholeProcessGroup pins the leak this exists to prevent: a
// CLI that starts a child of its own must not leave it running after the task
// is canceled. Killing only the leader would leave the grandchild behind.
func TestCancelStopsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	executor := NewExecutor(&Config{
		Command: "/bin/sh",
		Args: []string{"-c",
			"sleep 300 & echo $! > " + pidFile + "; wait"},
		Format: "text",
	})
	execCtx := &a2asrv.ExecutorContext{
		TaskID:    "task-cancel",
		ContextID: "ctx-cancel",
		Message:   a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart("hi")),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range executor.Execute(context.Background(), execCtx) {
		}
	}()

	// Wait for the child to exist before canceling, otherwise the test proves
	// nothing about grandchildren.
	var childPID int
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		childPID, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && childPID > 0
	}, 5*time.Second, 20*time.Millisecond, "the fake CLI never started its child")

	for range executor.Cancel(context.Background(), execCtx) {
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the execution did not stop after cancel")
	}

	// The child is gone: signal 0 asks "does this pid exist" without touching it.
	require.Eventually(t, func() bool {
		return syscall.Kill(childPID, 0) == syscall.ESRCH
	}, 5*time.Second, 20*time.Millisecond, "the CLI's child survived the cancel")
}
