// Command kubelistcheck is the go vet tool for the kubelistcheck analyzer.
//
// Usage:
//
//	go build -o _output/kubelistcheck ./hack/kubelistcheck
//	go vet -vettool=_output/kubelistcheck ./internal/... ./pkg/... ./cmd/...
//	_output/kubelistcheck -get ./internal/...
//
// It is a vet tool rather than a golangci-lint plugin because golangci-lint v2
// requires custom linters to be compiled into a dedicated binary through its
// own module system; `go vet -vettool` needs nothing beyond the standard
// toolchain and the repository's own dependencies.
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/onexstack/onexai/internal/lint/kubelistcheck"
)

func main() {
	singlechecker.Main(kubelistcheck.NewAnalyzer(false))
}
