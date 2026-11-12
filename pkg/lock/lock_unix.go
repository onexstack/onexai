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

package lock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// tryLock takes the exclusive advisory lock on file without waiting.
//
// flock rather than a lock file's mere existence: the kernel drops the lock
// when the process ends, so a crashed holder does not block the next start, and
// a lock file left behind says only who ran last.
func tryLock(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errors.New("the lock is held")
	}
	return fmt.Errorf("flock %s: %w", file.Name(), err)
}

// unlock releases the lock. Closing the file would release it too; unlocking is
// what makes the release visible to a runner-up before the descriptor goes.
func unlock(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("unlock %s: %w", file.Name(), err)
	}
	return nil
}
