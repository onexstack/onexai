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

// Package mirrorstore answers a resource List from the PostgreSQL read model
// instead of the informer cache.
//
// It is a read-only adapter. Nothing here writes, and nothing here is used by
// Get: a Get must be authoritative and is already O(1) against the cache or the
// API server, while a List is where the O(N) cost lives.
package mirrorstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/pkg/declclient/declstore"
	"github.com/onexstack/onexai/pkg/readmodel"
)

// CountMode selects how a List reports its total.
type CountMode string

const (
	// CountExact runs a count(*) over the filtered set.
	CountExact CountMode = "exact"
	// CountEstimate reads the projection's row count instead, for the unfiltered
	// case. At a million rows count(*) dominates the latency of a first page.
	CountEstimate CountMode = "estimate"
)

// Config describes one kind's projection.
type Config struct {
	DB     *gorm.DB
	Schema string
	Group  string
	Kind   string
	// MaxLag is how stale the projection may be before a List refuses to answer
	// from it.
	MaxLag    time.Duration
	CountMode CountMode
	// Now is injectable for tests.
	Now func() time.Time
}

// RawRow is one projected object as stored, before it is turned back into the
// store's row type.
type RawRow struct {
	Namespace       string
	Name            string
	UID             string
	ResourceVersion int64
	Generation      int64
	CreatedAt       time.Time
	ObservedAt      time.Time
	Enabled         *bool
	DisplayName     *string
	Owner           *string
	Labels          []byte
	Annotations     []byte
	Spec            []byte
	Status          []byte
}

// Reader answers one kind's List from the projection.
type Reader[M any] struct {
	db     *gorm.DB
	object string
	state  string
	group  string
	kind   string

	maxLag    time.Duration
	countMode CountMode
	now       func() time.Time
	decode    func(context.Context, RawRow) (M, error)

	// The freshness probe is memoised: a List is on the request path, and the
	// watermark only moves on the sweep interval, so re-reading it on every call
	// would add a round trip to answer a question that cannot have changed.
	mu        sync.Mutex
	checkedAt time.Time
	fresh     bool
	checkTTL  time.Duration
}

// New builds a reader for one kind.
func New[M any](cfg Config, decode func(context.Context, RawRow) (M, error)) (*Reader[M], error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("mirrorstore: no database configured")
	}
	if err := readmodel.ValidateSchema(cfg.Schema); err != nil {
		return nil, err
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	checkTTL := cfg.MaxLag / 4
	if checkTTL > 5*time.Second {
		checkTTL = 5 * time.Second
	}
	if checkTTL < 250*time.Millisecond {
		checkTTL = 250 * time.Millisecond
	}

	return &Reader[M]{
		db:        cfg.DB,
		object:    readmodel.Object(cfg.Schema),
		state:     readmodel.State(cfg.Schema),
		group:     cfg.Group,
		kind:      cfg.Kind,
		maxLag:    cfg.MaxLag,
		countMode: cfg.CountMode,
		now:       now,
		decode:    decode,
		checkTTL:  checkTTL,
	}, nil
}

// ListPage implements declstore.Reader.ListPage.
func (r *Reader[M]) ListPage(
	ctx context.Context, filters map[string]string, offset, limit int64,
) (int64, []M, error) {
	if err := r.ensureFresh(ctx); err != nil {
		return 0, nil, err
	}

	clause, filterArgs, err := r.where(filters)
	if err != nil {
		return 0, nil, err
	}

	var raw []RawRow
	query := fmt.Sprintf(`SELECT namespace, name, uid, resource_version, generation, created_at, observed_at,
       enabled, display_name, owner, labels, annotations, spec, status
FROM %s %s ORDER BY name ASC`, r.object, clause)
	// A copy, so appending the page bounds cannot scribble over filterArgs,
	// which the count below still needs.
	args := make([]any, len(filterArgs), len(filterArgs)+2)
	copy(args, filterArgs)
	if limit >= 0 {
		args = append(args, limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	if offset > 0 {
		args = append(args, offset)
		query += fmt.Sprintf(" OFFSET $%d", len(args))
	}
	if err := r.db.WithContext(ctx).Raw(query, args...).Scan(&raw).Error; err != nil {
		return 0, nil, fmt.Errorf("mirrorstore: list %s: %w", r.kind, err)
	}

	rows := make([]M, 0, len(raw))
	for _, item := range raw {
		row, err := r.decode(ctx, item)
		if err != nil {
			return 0, nil, err
		}
		rows = append(rows, row)
	}

	total, err := r.count(ctx, clause, filterArgs)
	if err != nil {
		return 0, nil, err
	}
	return total, rows, nil
}

// count returns the size of the filtered set.
func (r *Reader[M]) count(ctx context.Context, clause string, args []any) (int64, error) {
	if r.countMode == CountEstimate && len(args) == 2 {
		var state struct{ ObjectCount int64 }
		err := r.db.WithContext(ctx).
			Raw(fmt.Sprintf(`SELECT object_count FROM %s WHERE group_name = $1 AND kind = $2`, r.state),
				r.group, r.kind).
			Scan(&state).Error
		if err == nil && state.ObjectCount >= 0 {
			return state.ObjectCount, nil
		}
		// Fall through to an exact count: an estimate that cannot be read must not
		// turn into "zero rows".
	}

	var total int64
	err := r.db.WithContext(ctx).
		Raw(fmt.Sprintf(`SELECT count(*) FROM %s %s`, r.object, clause), args...).
		Scan(&total).Error
	if err != nil {
		return 0, fmt.Errorf("mirrorstore: count %s: %w", r.kind, err)
	}
	return total, nil
}

// where builds the predicate for a set of equality filters.
//
// The filter name is never interpolated: a promoted column is compared directly,
// everything else is read out of spec through a bind parameter, so an unexpected
// name can only fail to match — which is what the in-memory reader does with an
// unknown column too.
func (r *Reader[M]) where(filters map[string]string) (string, []any, error) {
	var (
		clauses = []string{"group_name = $1", "kind = $2"}
		args    = []any{r.group, r.kind}
	)
	for name, value := range filters {
		switch name {
		case "name":
			args = append(args, value)
			clauses = append(clauses, fmt.Sprintf("name = $%d", len(args)))
		case "namespace":
			args = append(args, value)
			clauses = append(clauses, fmt.Sprintf("namespace = $%d", len(args)))
		case "owner":
			args = append(args, value)
			clauses = append(clauses, fmt.Sprintf("owner = $%d", len(args)))
		case "display_name", "displayName":
			args = append(args, value)
			clauses = append(clauses, fmt.Sprintf("display_name = $%d", len(args)))
		case "enabled":
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				// An unparseable boolean matches nothing rather than everything.
				clauses = append(clauses, "false")
				continue
			}
			args = append(args, enabled)
			clauses = append(clauses, fmt.Sprintf("enabled = $%d", len(args)))
		default:
			args = append(args, name, value)
			clauses = append(clauses, fmt.Sprintf("spec->>$%d = $%d", len(args)-1, len(args)))
		}
	}
	return "WHERE " + strings.Join(clauses, " AND "), args, nil
}

// ensureFresh refuses to answer from a projection that has fallen behind.
func (r *Reader[M]) ensureFresh(ctx context.Context) error {
	now := r.now()

	r.mu.Lock()
	if !r.checkedAt.IsZero() && now.Sub(r.checkedAt) < r.checkTTL {
		fresh := r.fresh
		r.mu.Unlock()
		if !fresh {
			return declstore.ErrMirrorStale
		}
		return nil
	}
	r.mu.Unlock()

	var state struct{ UpdatedAt time.Time }
	err := r.db.WithContext(ctx).
		Raw(fmt.Sprintf(`SELECT updated_at FROM %s WHERE group_name = $1 AND kind = $2`, r.state),
			r.group, r.kind).
		Scan(&state).Error
	if err != nil {
		return fmt.Errorf("mirrorstore: read watermark for %s: %w", r.kind, err)
	}
	fresh := !state.UpdatedAt.IsZero() && now.Sub(state.UpdatedAt) <= r.maxLag

	r.mu.Lock()
	r.checkedAt, r.fresh = now, fresh
	r.mu.Unlock()

	if !fresh {
		return fmt.Errorf("%w: %s", declstore.ErrMirrorStale, r.kind)
	}
	return nil
}

// IsStale reports whether err is a staleness refusal. Callers that need to
// distinguish a fallback from a failure use it.
func IsStale(err error) bool { return errors.Is(err, declstore.ErrMirrorStale) }

// Meta rebuilds the ObjectMeta a store's row conversion reads.
//
// It lives here rather than in each store so the two control-plane packages that
// decode rows cannot drift on which metadata a projection restores.
func Meta(raw RawRow) metav1.ObjectMeta {
	meta := metav1.ObjectMeta{
		Name:      raw.Name,
		Namespace: raw.Namespace,
		Labels:    StringMap(raw.Labels),
	}
	if len(raw.Annotations) > 0 {
		_ = json.Unmarshal(raw.Annotations, &meta.Annotations)
	}
	if !raw.CreatedAt.IsZero() {
		meta.CreationTimestamp = metav1.NewTime(raw.CreatedAt)
	}
	return meta
}

// Unmarshal decodes the two jsonb payloads into the object's spec and status.
func Unmarshal(raw RawRow, spec, status any) error {
	if len(raw.Spec) > 0 {
		if err := json.Unmarshal(raw.Spec, spec); err != nil {
			return fmt.Errorf("mirrorstore: decode spec of %s/%s: %w", raw.Namespace, raw.Name, err)
		}
	}
	if len(raw.Status) > 0 {
		if err := json.Unmarshal(raw.Status, status); err != nil {
			return fmt.Errorf("mirrorstore: decode status of %s/%s: %w", raw.Namespace, raw.Name, err)
		}
	}
	return nil
}

// StringMap decodes a jsonb object of string values.
func StringMap(data []byte) map[string]string {
	if len(data) == 0 {
		return nil
	}
	values := map[string]string{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil
	}
	return values
}
