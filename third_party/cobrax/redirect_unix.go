//go:build unix

package cobrax

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// redirectStdio redirects os.Stdout/os.Stderr to the given pipe write ends at the
// fd level, so code holding init-time-captured *os.File references (e.g. Options.Out
// set to os.Stdout) also writes into the pipes. The returned restore function
// reinstates the original descriptors and is safe to call multiple times.
func redirectStdio(wOut, wErr *os.File) (restore func(), err error) {
	origStdoutFD, err := unix.Dup(unix.Stdout)
	if err != nil {
		return nil, fmt.Errorf("dup stdout: %w", err)
	}
	origStderrFD, err := unix.Dup(unix.Stderr)
	if err != nil {
		unix.Close(origStdoutFD)
		return nil, fmt.Errorf("dup stderr: %w", err)
	}
	if err := unix.Dup2(int(wOut.Fd()), unix.Stdout); err != nil {
		unix.Close(origStdoutFD)
		unix.Close(origStderrFD)
		return nil, fmt.Errorf("dup2 stdout: %w", err)
	}
	if err := unix.Dup2(int(wErr.Fd()), unix.Stderr); err != nil {
		unix.Close(origStdoutFD)
		unix.Close(origStderrFD)
		return nil, fmt.Errorf("dup2 stderr: %w", err)
	}

	var once sync.Once
	restore = func() {
		once.Do(func() {
			unix.Dup2(origStderrFD, unix.Stderr)
			unix.Close(origStderrFD)
			unix.Dup2(origStdoutFD, unix.Stdout)
			unix.Close(origStdoutFD)
		})
	}
	return restore, nil
}
