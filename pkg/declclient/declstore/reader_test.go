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

package declstore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"
)

func rows(values ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) { return values, nil }
}

func TestListPrefersThePagedBackend(t *testing.T) {
	var (
		gotFilters          map[string]string
		gotOffset, gotLimit int64
	)
	reader := Reader[string]{
		Key:    "name",
		KeyOf:  func(row string) string { return row },
		All:    rows("alpha", "beta"),
		Column: func(string, string) (string, bool) { return "", false },
		ListPage: func(_ context.Context, filters map[string]string, offset, limit int64) (int64, []string, error) {
			gotFilters, gotOffset, gotLimit = filters, offset, limit
			return 1, []string{"paged"}, nil
		},
	}

	total, listed, err := reader.List(context.Background(), where.NewWhere().F("namespace", "default").O(10).L(5))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(listed) != 1 || *listed[0] != "paged" {
		t.Fatalf("list = %d %v, want the paged backend's answer", total, listed)
	}
	if gotFilters["namespace"] != "default" || gotOffset != 10 || gotLimit != 5 {
		t.Fatalf("pushed down %v offset=%d limit=%d", gotFilters, gotOffset, gotLimit)
	}
}

func TestListFallsBackWhenTheBackendIsStale(t *testing.T) {
	reader := Reader[string]{
		Key:    "name",
		KeyOf:  func(row string) string { return row },
		All:    rows("alpha"),
		Column: func(string, string) (string, bool) { return "", false },
		ListPage: func(context.Context, map[string]string, int64, int64) (int64, []string, error) {
			return 0, nil, fmt.Errorf("%w: Agent", ErrMirrorStale)
		},
	}

	total, listed, err := reader.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || *listed[0] != "alpha" {
		t.Fatalf("list = %d %v, want the lister's answer", total, listed)
	}
}

func TestListSurfacesAnUnsupportedQueryRatherThanFallingBack(t *testing.T) {
	reader := Reader[string]{
		Key:    "name",
		KeyOf:  func(row string) string { return row },
		All:    rows("alpha"),
		Column: func(string, string) (string, bool) { return "", false },
		ListPage: func(context.Context, map[string]string, int64, int64) (int64, []string, error) {
			return 0, nil, errors.New("boom")
		},
	}

	if _, _, err := reader.List(context.Background(), nil); err == nil {
		t.Fatal("a backend failure was swallowed as a fallback")
	}
}

func TestListFallsBackWhenAFilterIsNotAString(t *testing.T) {
	reader := Reader[string]{
		Key:    "name",
		KeyOf:  func(row string) string { return row },
		All:    rows("alpha"),
		Column: func(string, string) (string, bool) { return "", false },
		ListPage: func(context.Context, map[string]string, int64, int64) (int64, []string, error) {
			t.Fatal("the backend was called with a filter it cannot express")
			return 0, nil, nil
		},
	}

	if _, _, err := reader.List(context.Background(), where.NewWhere().F("port", 8080)); err != nil {
		t.Fatalf("list: %v", err)
	}
}

func TestGetUsesTheKeyIndex(t *testing.T) {
	reader := Reader[string]{
		Key:    "name",
		KeyOf:  func(row string) string { return row },
		All:    rows("alpha", "beta"),
		Column: func(string, string) (string, bool) { return "", false },
		GetByKey: func(_ context.Context, key string) (string, bool, error) {
			return key + "-indexed", key == "beta", nil
		},
	}

	row, err := reader.Get(context.Background(), where.NewWhere().F("name", "beta"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row == nil || *row != "beta-indexed" {
		t.Fatalf("get = %v, want the indexed row", row)
	}

	// A miss falls through to the scan: the index may be namespace-scoped while
	// the scan is not.
	row, err = reader.Get(context.Background(), where.NewWhere().F("name", "alpha"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row == nil || *row != "alpha" {
		t.Fatalf("get = %v, want the scanned row", row)
	}
}

func TestGetDoesNotScanWhenTheProcessHoldsNoRows(t *testing.T) {
	reader := Reader[string]{
		Key:    "name",
		KeyOf:  func(row string) string { return row },
		NoScan: true,
		// Present but unreachable: with NoScan the scan must never run, because an
		// empty indexer would call every existing object missing.
		All:    rows("alpha"),
		Column: func(string, string) (string, bool) { return "", false },
		GetByKey: func(context.Context, string) (string, bool, error) {
			return "", false, nil
		},
	}

	row, err := reader.Get(context.Background(), where.NewWhere().F("name", "alpha"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row != nil {
		t.Fatalf("get = %v, want absent", *row)
	}
}

func TestListRefusesWhenNoBackendAnswersAndNoRowsAreHeld(t *testing.T) {
	reader := Reader[string]{
		Key:    "name",
		KeyOf:  func(row string) string { return row },
		NoScan: true,
		All:    rows("alpha"),
		Column: func(string, string) (string, bool) { return "", false },
		ListPage: func(context.Context, map[string]string, int64, int64) (int64, []string, error) {
			return 0, nil, fmt.Errorf("%w: Agent", ErrMirrorStale)
		},
	}

	if _, _, err := reader.List(context.Background(), nil); !errors.Is(err, ErrMirrorStale) {
		t.Fatalf("list = %v, want the backend's refusal instead of an empty page", err)
	}
}

// nsRow is a row with both a key and a namespace column.
type nsRow struct {
	Name      string
	Namespace string
}

func nsReader(rows ...nsRow) Reader[nsRow] {
	return Reader[nsRow]{
		Key:   "name",
		KeyOf: func(row nsRow) string { return row.Name },
		All: func(context.Context) ([]nsRow, error) {
			return rows, nil
		},
		Column: func(row nsRow, name string) (string, bool) {
			switch name {
			case "name":
				return row.Name, true
			case "namespace":
				return row.Namespace, true
			default:
				return "", false
			}
		},
	}
}

// Two namespaces may hold the same name. A Get that names one must not return
// the other; that ambiguity is exactly what the namespace filter removes.
func TestGetWithNamespacePicksTheRightRow(t *testing.T) {
	reader := nsReader(
		nsRow{Name: "gitlab", Namespace: "team-a"},
		nsRow{Name: "gitlab", Namespace: "platform"},
	)

	row, err := reader.Get(context.Background(), where.F("name", "gitlab").F("namespace", "platform"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row == nil || row.Namespace != "platform" {
		t.Fatalf("get = %v, want the platform copy", row)
	}
}

// Without a namespace filter a unique name still resolves, which keeps legacy
// bare entries working. Ambiguity is refused separately.
func TestGetWithoutNamespaceKeepsTheOldLookup(t *testing.T) {
	row, err := nsReader(nsRow{Name: "gitlab", Namespace: "team-a"}).
		Get(context.Background(), where.F("name", "gitlab"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row == nil || row.Namespace != "team-a" {
		t.Fatalf("get = %v, want the bare-name lookup", row)
	}
}

// A bare name that exists in two namespaces is refused. Returning either row
// would be a silent cross-tenant choice; the caller has to name the namespace.
func TestGetWithoutNamespaceRefusesAmbiguity(t *testing.T) {
	_, err := nsReader(
		nsRow{Name: "gitlab", Namespace: "team-a"},
		nsRow{Name: "gitlab", Namespace: "platform"},
	).Get(context.Background(), where.F("name", "gitlab"))
	if !errors.Is(err, ErrAmbiguousKey) {
		t.Fatalf("get = %v, want ErrAmbiguousKey", err)
	}
	var ambiguous *AmbiguousKeyError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("get = %v, want the typed error", err)
	}
	if ambiguous.Key != "gitlab" || len(ambiguous.Namespaces) != 2 {
		t.Fatalf("ambiguous = %+v, want the key and both namespaces", ambiguous)
	}
}

// The namespaced index is the fast path; a filter it cannot answer must fall
// back to the scan rather than silently widen the lookup.
func TestGetPrefersTheNamespacedIndex(t *testing.T) {
	var gotNamespace, gotKey string
	reader := nsReader(nsRow{Name: "gitlab", Namespace: "platform"})
	reader.GetByKey = func(context.Context, string) (nsRow, bool, error) {
		t.Fatal("the bare index must not answer a namespaced lookup")
		return nsRow{}, false, nil
	}
	reader.GetByNamespacedKey = func(_ context.Context, namespace, key string) (nsRow, bool, error) {
		gotNamespace, gotKey = namespace, key
		return nsRow{Name: key, Namespace: namespace}, true, nil
	}

	row, err := reader.Get(context.Background(), where.F("name", "gitlab").F("namespace", "platform"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row == nil || gotNamespace != "platform" || gotKey != "gitlab" {
		t.Fatalf("get = %v, index saw namespace=%q key=%q", row, gotNamespace, gotKey)
	}
}

// A filter the reader cannot express is refused, not ignored: answering a
// narrower question with a wider row set is how a tenant boundary leaks.
func TestGetRefusesAnUnknownFilter(t *testing.T) {
	_, err := nsReader(nsRow{Name: "gitlab", Namespace: "platform"}).
		Get(context.Background(), where.F("name", "gitlab").F("workspace", "team-a"))
	if !errors.Is(err, ErrUnsupportedQuery) {
		t.Fatalf("get = %v, want ErrUnsupportedQuery", err)
	}
}
