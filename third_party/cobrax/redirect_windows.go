//go:build windows

package cobrax

import "os"

// redirectStdio is a no-op on Windows: fd-level redirection is unsupported, and
// SetOut/SetErr already capture command output.
func redirectStdio(wOut, wErr *os.File) (restore func(), err error) {
	return func() {}, nil
}
