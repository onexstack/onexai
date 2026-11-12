// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// Package kubelistcheck reports list calls that cannot be served from the
// apiserver's watch cache.
//
// A List whose ListOptions carry no ResourceVersion is a consistent read: the
// apiserver must ask etcd, which serializes on the range and takes a quorum
// round trip. ResourceVersion "0" asks for "any version you have" and is
// answered from the watch cache instead. The two differ only by a string
// literal, so a missing one costs a quorum read per reconcile and looks exactly
// like working code.
//
// The check runs over a service's own packages, not generated clients.
package kubelistcheck

import (
	"flag"
	"go/ast"
	"go/types"
	"regexp"
	"slices"
	"strings"
	"sync"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

var (
	checkAll  = regexp.MustCompile(`^k8s.io/apimachinery/pkg/apis/meta/v1\.(?:ListOptions|GetOptions)$`)
	checkList = regexp.MustCompile(`^k8s.io/apimachinery/pkg/apis/meta/v1\.ListOptions$`)
)

// cacheFields are the fields that make a list cache-servable: ResourceVersion
// picks the cache directly, and Continue carries the version of the page it
// resumes from. Setting neither is the case this check exists for.
var cacheFields = []string{"ResourceVersion", "Continue"}

// NewAnalyzer returns a go/analysis-compatible analyzer.
//
// The -get flag extends the check to GetOptions, where the same rule applies:
// an unset ResourceVersion forces a quorum read.
func NewAnalyzer(get bool) *analysis.Analyzer {
	a := &analyzer{ //nolint:exhaustruct_v5 // the mutex needs no initialization
		get:               get,
		typesProcessCache: map[types.Type]bool{},
	}

	fs := flag.NewFlagSet("kubelistcheck flags", flag.PanicOnError)
	fs.BoolVar(&a.get, "get", get, "also check GetOptions, not just ListOptions")

	return &analysis.Analyzer{ //nolint:exhaustruct_v5 // only the fields an analyzer needs
		Name:     "kubelistcheck",
		Doc:      "reports Kubernetes list/get calls that bypass the apiserver watch cache",
		Run:      a.run,
		Requires: []*analysis.Analyzer{inspect.Analyzer},
		Flags:    *fs,
	}
}

type analyzer struct {
	get bool

	typesProcessCache   map[types.Type]bool
	typesProcessCacheMu sync.RWMutex
}

func (a *analyzer) run(pass *analysis.Pass) (any, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector) //nolint:forcetypeassert

	insp.Preorder([]ast.Node{(*ast.CompositeLit)(nil), (*ast.ReturnStmt)(nil)}, a.newVisitor(pass))

	return nil, nil //nolint:nilnil // an analyzer without a result
}

// The visitor walks two node kinds and returns early on most of them; a
// table of checks would obscure which node it is looking at.
//
//nolint:gocyclo,cyclop,gocognit // one linear walk over two node kinds
func (a *analyzer) newVisitor(pass *analysis.Pass) func(node ast.Node) {
	var ret *ast.ReturnStmt

	return func(node ast.Node) {
		// Tests drive fake clients: there is no apiserver cache to bypass, and
		// requiring a ResourceVersion in them would be noise.
		if file := pass.Fset.File(node.Pos()); file != nil && strings.HasSuffix(file.Name(), "_test.go") {
			return
		}

		if hasNolint(pass, node, "kubelistcheck") {
			return
		}

		if retStmt, ok := node.(*ast.ReturnStmt); ok {
			// Remembered so an empty literal returned alongside an error can be
			// exempted below.
			ret = retStmt

			return
		}

		lit, ok := node.(*ast.CompositeLit)
		if !ok || lit.Type == nil {
			return
		}

		typ := pass.TypesInfo.TypeOf(lit.Type)
		if typ == nil {
			return
		}

		if _, isStruct := typ.Underlying().(*types.Struct); !isStruct {
			return
		}

		name := exprName(lit.Type)
		if name == "" || !a.shouldProcessType(typ) {
			return
		}

		if len(lit.Elts) == 0 && ret != nil {
			if ret.End() < lit.Pos() {
				// Past the last return statement: it cannot be a return value.
				ret = nil
			} else if returnContainsLiteral(ret, lit) && returnContainsError(ret, pass) {
				// `return X{}, err` is an empty value next to a failure. Nothing
				// is listed, so there is no cache to bypass.
				return
			}
		}

		if !hasAnyField(lit, cacheFields) {
			pass.Reportf(lit.Pos(),
				"%s does not set ResourceVersion: this call bypasses the watch cache and reads etcd; "+
					"set ResourceVersion: \"0\" (any version) or Continue", name)
		}
	}
}

func (a *analyzer) shouldProcessType(typ types.Type) bool {
	a.typesProcessCacheMu.RLock()

	matches, ok := a.typesProcessCache[typ]
	a.typesProcessCacheMu.RUnlock()

	if ok {
		return matches
	}

	a.typesProcessCacheMu.Lock()
	defer a.typesProcessCacheMu.Unlock()

	reg := checkList
	if a.get {
		reg = checkAll
	}

	matches = reg.MatchString(typ.String())
	a.typesProcessCache[typ] = matches

	return matches
}

func returnContainsLiteral(ret *ast.ReturnStmt, lit *ast.CompositeLit) bool {
	for _, result := range ret.Results {
		if l, ok := result.(*ast.CompositeLit); ok && l == lit {
			return true
		}
	}

	return false
}

func returnContainsError(ret *ast.ReturnStmt, pass *analysis.Pass) bool {
	for _, result := range ret.Results {
		if t := pass.TypesInfo.TypeOf(result); t != nil && t.String() == "error" {
			return true
		}
	}

	return false
}

func exprName(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}

	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}

	return sel.Sel.Name
}

// hasAnyField reports whether the literal sets at least one of names.
func hasAnyField(lit *ast.CompositeLit, names []string) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		ident, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}

		if slices.Contains(names, ident.Name) {
			return true
		}
	}

	return false
}

// hasNolint reports whether node carries a //nolint directive naming this
// analyzer.
//
// It parses the directive as a list rather than looking for the substring
// "nolint:kubelistcheck": the common form is //nolint:exhaustruct,kubelistcheck,
// where a substring search would silently stop matching as soon as another
// linter is named first. The comment may sit on the node's own line or on the
// line above it.
// It walks the file's comment groups, which is inherently nested.
//
//nolint:gocognit // comment-map walking
func hasNolint(pass *analysis.Pass, node ast.Node, name string) bool {
	file := pass.Fset.File(node.Pos())
	if file == nil {
		return false
	}

	first := file.Line(node.Pos())
	last := file.Line(node.End())

	for _, f := range pass.Files {
		if pass.Fset.File(f.Pos()) != file {
			continue
		}

		for _, group := range f.Comments {
			for _, comment := range group.List {
				line := file.Line(comment.Pos())
				if line < first-1 || line > last {
					continue
				}

				if nolintNames(comment.Text, name) {
					return true
				}
			}
		}
	}

	return false
}

// nolintNames reports whether a comment's //nolint directive lists name.
func nolintNames(comment, name string) bool {
	const marker = "nolint:"

	_, list, ok := strings.Cut(comment, marker)
	if !ok {
		return false
	}

	// Everything up to the first blank separates the list from an explanation:
	// "//nolint:a,b // because ...".
	if j := strings.IndexAny(list, " \t"); j >= 0 {
		list = list[:j]
	}

	for part := range strings.SplitSeq(list, ",") {
		if strings.TrimSpace(part) == name {
			return true
		}
	}

	return false
}
