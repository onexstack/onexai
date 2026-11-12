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

// Package declstore adapts a control-plane lister to the where-shaped store
// surface the services already expose.
//
// The store interfaces were written against SQL, so their reads take a
// where.Options whose Filters are column equality predicates. A Reader honours
// exactly that subset plus offset/limit and refuses everything else with
// ErrUnsupportedQuery: a query that needs SQL must fail loudly rather than
// quietly return a different row set.
package declstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/onexstack/onexstack/pkg/store/where"
	"k8s.io/client-go/util/retry"
)

// ErrUnsupportedQuery reports a query shape the control plane cannot express.
var ErrUnsupportedQuery = errors.New("declstore: query shape is not supported by the control plane")

// ErrManagedByControlPlane reports a write the API server owns. Resources on the
// control plane are created and changed through the Kubernetes API, where
// validation, dry-run and audit live; a store write would bypass all three.
var ErrManagedByControlPlane = errors.New("declstore: the resource is managed through the control plane")

// ErrMirrorStale reports a query backend that has fallen behind the control
// plane. A List falls back to the lister when it sees this: answering from a
// stale projection silently is worse than a slower answer.
var ErrMirrorStale = errors.New("declstore: the query backend is behind the control plane")

// ErrAmbiguousKey reports a lookup by key alone that matched more than one
// namespace. Picking one would be a silent cross-tenant choice, so the read
// fails instead; the caller either names the namespace or asks the user to.
var ErrAmbiguousKey = errors.New("declstore: key matches more than one namespace")

// AmbiguousKeyError carries the key and the candidate namespaces for a caller
// that wants to explain the refusal. The message deliberately names the key and
// the namespaces: the caller is an operator of the same platform, and "use
// namespace/name" is not actionable without knowing which namespaces matched.
type AmbiguousKeyError struct {
	Key        string
	Namespaces []string
}

func (e *AmbiguousKeyError) Error() string {
	return fmt.Sprintf("declstore: %q matches multiple namespaces (%s); use namespace/name",
		e.Key, strings.Join(e.Namespaces, ", "))
}

// Unwrap makes errors.Is(err, ErrAmbiguousKey) work for the typed error.
func (e *AmbiguousKeyError) Unwrap() error { return ErrAmbiguousKey }

// Column reports the value of a named column for filtering. The second result is
// false when the row has no such column, which makes a filter on an unknown
// column match nothing instead of matching everything.
type Column[M any] func(row M, name string) (string, bool)

// Reader serves Get and List over an in-memory set.
type Reader[M any] struct {
	// All returns the current rows. It is called per read; the lister behind it
	// is already a cache, so there is nothing to memoise here.
	All func(ctx context.Context) ([]M, error)
	// Key is the name of the table's unique column, the one a Get names.
	Key string
	// KeyOf returns that column's value, used for ordering and for Get.
	KeyOf func(M) string
	// Column reads a filterable column.
	Column Column[M]
	// GetByKey, when set, returns the single row with this key and whether it
	// exists. It lets a Get use the lister's index instead of materialising every
	// row — which matters for rows that enrich themselves on the way out (a model
	// row reads a Secret).
	GetByKey func(ctx context.Context, key string) (M, bool, error)
	// GetByNamespacedKey, when set, returns the single row with this
	// (namespace, key) pair. It is what makes cross-namespace references
	// addressable: a bare name is ambiguous once two namespaces may hold the
	// same one.
	GetByNamespacedKey func(ctx context.Context, namespace, key string) (M, bool, error)
	// ListPage, when set, pushes the filters and the page down to a query
	// backend. Nil means List filters All() in memory, which is what it has
	// always done; that fallback also covers a backend that cannot express the
	// query (ErrUnsupportedQuery) or has fallen behind (ErrMirrorStale).
	ListPage func(ctx context.Context, filters map[string]string, offset, limit int64) (int64, []M, error)
	// NoScan reports that this process does not hold the rows at all: Get is
	// answered by GetByKey alone, and a miss means the row is genuinely absent
	// rather than "the scan did not find it". It is what makes dropping the
	// informer safe — without it, a Get that missed the index would fall through
	// to an empty scan and call every existing object missing.
	NoScan bool
}

// Get returns the row whose key column equals the filter in opts. An optional
// "namespace" filter narrows the lookup to that namespace.
//
// A name the set does not hold answers (nil, nil), not a not-found error: the
// callers that can distinguish "absent" from "unreadable" do so, and the write
// path relies on it to fall through to a create.
func (r Reader[M]) Get(ctx context.Context, opts *where.Options) (*M, error) {
	key, namespace, ok := keyAndNamespace(opts, r.Key)
	if !ok {
		return nil, fmt.Errorf("%w: Get needs an equality filter on the key column and at most one namespace filter", ErrUnsupportedQuery)
	}

	if namespace != "" && r.GetByNamespacedKey != nil {
		row, found, err := r.GetByNamespacedKey(ctx, namespace, key)
		if err != nil {
			return nil, err
		}
		if found {
			return &row, nil
		}
		if r.NoScan {
			return nil, nil
		}
	} else if r.GetByKey != nil {
		row, found, err := r.GetByKey(ctx, key)
		if err != nil {
			return nil, err
		}
		if found {
			return &row, nil
		}
		if r.NoScan {
			// Nothing to fall through to: the index is the whole truth here.
			return nil, nil
		}
		// Fall through: the fast path may be namespace-scoped while the scan is
		// not, and narrowing a lookup silently would be worse than a slow one.
	}
	if r.All == nil {
		return nil, nil
	}

	rows, err := r.All(ctx)
	if err != nil {
		return nil, err
	}
	var match *M
	var namespaces []string
	for i := range rows {
		if r.KeyOf(rows[i]) != key {
			continue
		}
		if namespace != "" {
			if r.Column == nil {
				continue
			}
			got, known := r.Column(rows[i], "namespace")
			if !known || got != namespace {
				continue
			}
		}
		if match == nil {
			match = &rows[i]
		}
		if r.Column != nil {
			if got, known := r.Column(rows[i], "namespace"); known && got != "" {
				namespaces = append(namespaces, got)
			}
		}
	}
	// A lookup by key alone must answer one object. Two namespaces holding the
	// same key is not a tie to break: returning either one would silently hand
	// a caller another tenant's object, which is the class of bug the
	// namespace column exists to prevent.
	if namespace == "" && len(namespaces) > 1 {
		sort.Strings(namespaces)
		return nil, &AmbiguousKeyError{Key: key, Namespaces: dedupeStrings(namespaces)}
	}
	return match, nil
}

// dedupeStrings removes adjacent duplicates from a sorted slice.
func dedupeStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

// List returns the rows matching opts' equality filters, paged by opts.
//
// It prefers ListPage when the reader has one, because that backend answers in
// O(page) while the in-memory path below is O(N) per call: it materialises every
// row, sorts them and only then slices the page. A backend that cannot express
// the query, or has fallen behind, is not an error — the lister is still there
// and answering from it is exactly what this reader did before the projection
// existed.
func (r Reader[M]) List(ctx context.Context, opts *where.Options) (int64, []*M, error) {
	if opts != nil && (len(opts.Queries) > 0 || len(opts.Clauses) > 0) {
		return 0, nil, fmt.Errorf("%w: List cannot express raw queries or clauses", ErrUnsupportedQuery)
	}

	if r.ListPage != nil {
		if filters, ok := stringFilters(opts); ok {
			total, rows, err := r.ListPage(ctx, filters, offsetOf(opts), limitOf(opts))
			switch {
			case err == nil:
				return total, toPointers(rows), nil
			case errors.Is(err, ErrUnsupportedQuery), errors.Is(err, ErrMirrorStale):
				if r.NoScan {
					// There is no lister to fall back to: the caller must see the
					// refusal rather than an empty page.
					return 0, nil, err
				}
				// Fall through to the lister.
			default:
				return 0, nil, err
			}
		}
	}

	if r.NoScan || r.All == nil {
		// No rows are held and no backend answered: refuse rather than return an
		// empty page, which a caller cannot tell from "nothing matches".
		return 0, nil, fmt.Errorf("%w: no list backend is configured", ErrUnsupportedQuery)
	}

	rows, err := r.All(ctx)
	if err != nil {
		return 0, nil, err
	}

	matched := make([]*M, 0, len(rows))
	for i := range rows {
		if r.matches(rows[i], opts) {
			matched = append(matched, &rows[i])
		}
	}

	sort.SliceStable(matched, func(i, j int) bool { return r.KeyOf(*matched[i]) < r.KeyOf(*matched[j]) })

	total := int64(len(matched))
	if opts != nil && opts.Offset > 0 {
		if opts.Offset >= len(matched) {
			matched = nil
		} else {
			matched = matched[opts.Offset:]
		}
	}
	if opts != nil && opts.Limit > 0 && opts.Limit < len(matched) {
		matched = matched[:opts.Limit]
	}

	return total, matched, nil
}

func (r Reader[M]) matches(row M, opts *where.Options) bool {
	if opts == nil {
		return true
	}
	for key, want := range opts.Filters {
		name, ok := key.(string)
		if !ok {
			return false
		}
		text, ok := want.(string)
		if !ok {
			return false
		}
		got, known := r.Column(row, name)
		if !known || got != text {
			return false
		}
	}
	return true
}

// stringFilters turns opts' equality filters into the map a query backend takes.
//
// A filter whose value is not a string cannot be pushed down as an equality, so
// the whole query falls back rather than silently dropping that predicate.
func stringFilters(opts *where.Options) (map[string]string, bool) {
	if opts == nil || len(opts.Filters) == 0 {
		return nil, true
	}
	filters := make(map[string]string, len(opts.Filters))
	for key, value := range opts.Filters {
		name, ok := key.(string)
		if !ok {
			return nil, false
		}
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		filters[name] = text
	}
	return filters, true
}

func offsetOf(opts *where.Options) int64 {
	if opts == nil {
		return 0
	}
	return int64(opts.Offset)
}

func limitOf(opts *where.Options) int64 {
	if opts == nil {
		return -1
	}
	return int64(opts.Limit)
}

// toPointers returns stable pointers into the slice, not into a loop variable.
func toPointers[M any](rows []M) []*M {
	pointers := make([]*M, len(rows))
	for i := range rows {
		pointers[i] = &rows[i]
	}
	return pointers
}

// SingleKey returns the requested key when opts carries exactly one equality
// filter on keyColumn and nothing SQL-only.
func SingleKey(opts *where.Options, keyColumn string) (string, bool) {
	if opts == nil || len(opts.Filters) != 1 || len(opts.Queries) > 0 || len(opts.Clauses) > 0 {
		return "", false
	}
	for key, value := range opts.Filters {
		name, ok := key.(string)
		if !ok || name != keyColumn {
			return "", false
		}
		text, ok := value.(string)
		return text, ok
	}
	return "", false
}

// keyAndNamespace returns the requested key and the optional namespace filter.
//
// The shape it accepts is deliberately narrow: exactly one equality filter on
// the key column, and optionally one on "namespace". Anything else is a query
// the control plane cannot express, and refusing is better than returning a
// different row than the caller asked for.
func keyAndNamespace(opts *where.Options, keyColumn string) (key, namespace string, ok bool) {
	if opts == nil || len(opts.Queries) > 0 || len(opts.Clauses) > 0 {
		return "", "", false
	}
	seenKey := false
	for rawKey, value := range opts.Filters {
		name, isString := rawKey.(string)
		if !isString {
			return "", "", false
		}
		text, isText := value.(string)
		if !isText {
			return "", "", false
		}
		switch name {
		case keyColumn:
			if seenKey {
				return "", "", false
			}
			key, seenKey = text, true
		case "namespace":
			if namespace != "" {
				return "", "", false
			}
			namespace = text
		default:
			return "", "", false
		}
	}
	if !seenKey {
		return "", "", false
	}
	return key, namespace, true
}

// Store serves one table's five CRUD methods over the control plane.
type Store[M any] struct {
	Reader[M]

	CreateFunc func(ctx context.Context, row *M) error
	UpdateFunc func(ctx context.Context, row *M) error
	DeleteFunc func(ctx context.Context, name string) error
}

// NewStore builds a store from a reader and the three write callbacks.
func NewStore[M any](reader Reader[M], create, update func(context.Context, *M) error, delete func(context.Context, string) error) *Store[M] {
	return &Store[M]{Reader: reader, CreateFunc: create, UpdateFunc: update, DeleteFunc: delete}
}

// Create writes a new object.
func (s *Store[M]) Create(ctx context.Context, row *M) error { return s.CreateFunc(ctx, row) }

// Update rewrites an existing object.
//
// The write retries on a resourceVersion conflict: the update callback re-reads
// the object before writing, so another writer (a controller patching status, or
// a second replica) only costs a retry rather than a failed call.
func (s *Store[M]) Update(ctx context.Context, row *M) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return s.UpdateFunc(ctx, row)
	})
}

// Delete removes the object named by opts' key filter.
func (s *Store[M]) Delete(ctx context.Context, opts *where.Options) error {
	name, ok := SingleKey(opts, s.Key)
	if !ok {
		return ErrUnsupportedQuery
	}
	return s.DeleteFunc(ctx, name)
}
