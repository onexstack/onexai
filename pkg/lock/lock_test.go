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

package lock

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// holder is the label these tests use. The label's only job is to appear in the
// error, which is what makes "another X is already running" actionable.
const holder = "oai-chat-apiserver"

// TestTheLockExcludes pins the rule a single-instance program refuses to start
// twice under: the second start is an error, and the error names the holder and
// the lock, because "already running" without a pid is a message an operator
// cannot act on.
func TestTheLockExcludes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	first, err := Acquire(path, holder)
	if err != nil {
		t.Fatalf("first Acquire() = %v, want the lock", err)
	}
	defer first.Release() //nolint:errcheck

	if got := readPID(t, path); got == 0 {
		t.Error("the lock file does not record the holder, so the error cannot name it")
	}

	_, err = Acquire(path, holder)
	if err == nil {
		t.Fatal("a second Acquire() succeeded on a held lock")
	}
	for _, want := range []string{holder, path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
	// The pid is the part an operator uses: it is what `kill` takes, and what
	// tells them whether the holder is the instance they thought.
	if got := readPID(t, path); !strings.Contains(err.Error(), fmt.Sprint(got)) {
		t.Errorf("error = %v, want it to name the holding pid %d", err, got)
	}
}

// TestTheLockIsReleased pins the half that makes the lock usable: a graceful
// stop must let the next start through. A lock that outlived its holder would
// turn every restart into an outage.
func TestTheLockIsReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	first, err := Acquire(path, holder)
	if err != nil {
		t.Fatalf("Acquire() = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	// Releasing twice is not an error: a deferred release and an explicit one
	// both run on some paths.
	if err := first.Release(); err != nil {
		t.Errorf("second Release() = %v, want nil", err)
	}

	second, err := Acquire(path, holder)
	if err != nil {
		t.Fatalf("Acquire() after release = %v, want the lock", err)
	}
	defer second.Release() //nolint:errcheck
}

// TestTheLockIsHeldAgainstAnotherProcess pins what the lock is actually for. A
// test in one process can only show that two acquisitions conflict; the case
// that matters is a lock held by a process that is still running, so this asks
// a real second process to try.
func TestTheLockIsHeldAgainstAnotherProcess(t *testing.T) {
	if os.Getenv("ONEXAI_LOCK_TEST_CHILD") != "" {
		// The child half: try to take the lock the parent holds.
		if _, err := Acquire(os.Getenv("ONEXAI_LOCK_TEST_CHILD"), holder); err == nil {
			os.Exit(0)
		}
		os.Exit(3)
	}

	path := filepath.Join(t.TempDir(), "instance.lock")
	parent, err := Acquire(path, holder)
	if err != nil {
		t.Fatalf("Acquire() = %v", err)
	}
	defer parent.Release() //nolint:errcheck

	cmd := exec.Command(os.Args[0], "-test.run=TestTheLockIsHeldAgainstAnotherProcess")
	cmd.Env = append(os.Environ(), "ONEXAI_LOCK_TEST_CHILD="+path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a second process took the lock the test process holds (output %q)", out)
	}
	if code := cmd.ProcessState.ExitCode(); code != 3 {
		t.Fatalf("child exited %d (%q), want 3: the lock was neither taken nor refused", code, out)
	}
}

// TestTheLockOutlivesACrash is what an advisory lock buys over a pid file: the
// kernel drops it when the holder dies however it dies, so a lock left on disk
// by a killed process does not block the next start.
func TestTheLockOutlivesACrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	dead := filepath.Join(t.TempDir(), "dead.pid")

	// The name is deliberately unlike the parent's: -test.run is an unanchored
	// pattern, so a name the parent's pattern also matches would re-run the
	// parent test in the child and recurse.
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldsTheLock")
	cmd.Env = append(os.Environ(), "ONEXAI_LOCK_TEST_HOLD="+path, "ONEXAI_LOCK_TEST_PID="+dead)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the holder: %v", err)
	}

	// Wait for the child to have taken the lock, so that killing it here kills a
	// holder rather than a process that had not got there yet.
	waitForPIDFile(t, dead)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the holder: %v", err)
	}
	_ = cmd.Wait()

	if _, err := Acquire(path, holder); err != nil {
		t.Errorf("Acquire() after the holder was killed = %v, want the lock", err)
	}
}

// TestHelperHoldsTheLock is the child half of TestTheLockOutlivesACrash. It is
// not a test of anything: it takes a lock, says it has it, and waits to be
// killed, which is the only state the parent cannot create by itself.
func TestHelperHoldsTheLock(t *testing.T) {
	path := os.Getenv("ONEXAI_LOCK_TEST_HOLD")
	if path == "" {
		t.Skip("not the holding child")
	}
	l, err := Acquire(path, holder)
	if err != nil {
		os.Exit(4)
	}
	defer l.Release() //nolint:errcheck
	if err := os.WriteFile(os.Getenv("ONEXAI_LOCK_TEST_PID"), []byte(fmt.Sprint(os.Getpid())), 0o644); err != nil {
		os.Exit(5)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func waitForPIDFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the holder never took the lock")
}

// readPID returns the pid recorded in the lock file, or 0 when none is.
func readPID(t *testing.T, path string) int {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the lock file: %v", err)
	}
	pid := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil {
		t.Fatalf("lock file %q does not hold a pid: %v", data, err)
	}
	return pid
}
