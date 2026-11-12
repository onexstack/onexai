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

package lock

import (
	"errors"
	"os"
)

// tryLock degrades to "does the file exist", which does not survive a crash.
//
// The alternative is refusing to start on a platform where the callers are not
// built for production anyway — the chat service's local backend spawns and
// signals a process group, which is unix-only — and refusing would make the
// rest of the program unusable to say so. So the lock is taken as best it can
// be, and a deployment on this platform is expected to keep one instance.
func tryLock(file *os.File) error {
	_, err := file.Stat()
	return err
}

// unlock has nothing to release on this platform.
func unlock(file *os.File) error {
	if file == nil {
		return errors.New("nothing to unlock")
	}
	return nil
}
