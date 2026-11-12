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

// Package lock holds an advisory file lock that says one instance of some
// program is running.
//
// # Why a file lock and not a pid file
//
// The kernel releases an advisory lock when the process ends, however it ends.
// A pid file does not: a crashed holder leaves a file that says a process is
// running when nothing is, and the next start either refuses to run or has to
// guess whether the pid it read still means anything. The pid is still written
// here, but only so that the error can name the holder — the lock is what
// decides.
//
// The lock is per path and per machine, and it is deliberately not reentrant:
// two locks on one path in one process conflict, which is the right answer for
// "am I already running", where a second acquisition is a bug rather than a
// nested request.
package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Lock is a held lock.
type Lock struct {
	path string
	file *os.File
}

// Acquire takes the exclusive lock on path, creating the file and its directory
// if they do not exist. It fails when another process already holds it.
//
// holder names the thing being locked, and is used only to say so in the error:
// "another oai-chat-apiserver (pid 1234) is already running" is a message an
// operator can act on, and "the lock is held" is not.
func Acquire(path, holder string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create the lock directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the lock %s: %w", path, err)
	}

	if err := tryLock(file); err != nil {
		who := readHolder(file)
		_ = file.Close()
		if who == "" {
			return nil, fmt.Errorf("another %s is already running (lock %s): %w", holder, path, err)
		}
		return nil, fmt.Errorf("another %s (pid %s) is already running (lock %s): %w",
			holder, who, path, err)
	}

	// Written after the lock is held, so a reader never sees a pid that does not
	// hold it: the loser's write would otherwise race the winner's.
	if err := writeHolder(file); err != nil {
		_ = file.Close()
		return nil, err
	}

	return &Lock{path: path, file: file}, nil
}

// Release drops the lock. It is safe to call more than once, and on a nil Lock.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	// The lock goes with the descriptor, so unlocking explicitly is a courtesy
	// to a reader that happens to look in the window before the close.
	err := unlock(file)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// Path is the file the lock is held on.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// writeHolder records this process's PID in the lock file.
func writeHolder(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate the lock: %w", err)
	}
	if _, err := file.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		return fmt.Errorf("write the lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync the lock: %w", err)
	}
	return nil
}

// readHolder returns the PID recorded in the lock file, or "" when it cannot be
// read. It is used for the error message only.
func readHolder(file *os.File) string {
	buf := make([]byte, 32)
	n, _ := file.ReadAt(buf, 0)
	if n <= 0 {
		return ""
	}
	return strings.TrimSpace(string(buf[:n]))
}
