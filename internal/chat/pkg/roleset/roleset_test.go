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

package roleset

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeSource stands in for the store: the cache only ever asks it for every
// setting row, so that is all it implements. calls is what proves the getters
// never touch it.
type fakeSource struct {
	rows  []*Row
	err   error
	calls int
}

func (f *fakeSource) ListAll(_ context.Context) ([]*Row, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func row(key, value string) *Row {
	return &Row{Key: key, Value: value}
}

// TestParseIDsJSONArray pins the canonical value form.
func TestParseIDsJSONArray(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"an array of ids", `["user_id_a","user_id_b"]`, []string{"user_id_a", "user_id_b"}},
		{"surrounding whitespace is dropped", `  [" a ", "b"]  `, []string{"a", "b"}},
		{"blanks and duplicates collapse", `["a","","a","  "]`, []string{"a"}},
		{"an empty array grants nobody", `[]`, nil},
		{"a JSON null grants nobody", `null`, nil},
		{"an empty value grants nobody", ``, nil},
		{"a blank value grants nobody", `   `, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseIDs(context.Background(), KeyAdminUserIDs, tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parseIDs(%q) = %d ids, want %d", tt.raw, len(got), len(tt.want))
			}
			for _, id := range tt.want {
				if _, ok := got[id]; !ok {
					t.Errorf("parseIDs(%q) is missing %q", tt.raw, id)
				}
			}
		})
	}
}

// TestParseIDsCommaFallback pins the tolerance that keeps a hand-written row
// usable: the value is written with SQL, and a missing pair of brackets must not
// turn into "nobody is an administrator". The old config keys were comma
// separated, so this is also the shape an operator is likeliest to type.
func TestParseIDsCommaFallback(t *testing.T) {
	got := parseIDs(context.Background(), KeySREUserIDs, " ou_a ,, ou_b ")
	if len(got) != 2 {
		t.Fatalf("comma-separated value = %d ids, want 2", len(got))
	}
	for _, id := range []string{"ou_a", "ou_b"} {
		if _, ok := got[id]; !ok {
			t.Errorf("comma-separated value is missing %q", id)
		}
	}

	// A value that parses as neither form cannot name a real user: the fallback
	// keeps the broken text as a single id, which matches nothing. What matters
	// is that the id written inside the broken JSON did not slip through.
	if ids := parseIDs(context.Background(), KeySREUserIDs, `{"sre": ["ou_a"]}`); len(ids) != 0 {
		if _, ok := ids["ou_a"]; ok {
			t.Error("a malformed value granted ou_a, want nobody")
		}
	}
}

// TestRefreshUnionsSREIntoAdmin pins the relationship between the two lists: an
// SRE is an administrator, but an administrator is not an SRE. The roles gate
// different commands, so the two sets are kept separately — collapsing them
// would turn every x-sre-only command into an x-admin-only one.
func TestRefreshUnionsSREIntoAdmin(t *testing.T) {
	src := &fakeSource{rows: []*Row{
		row(KeySREUserIDs, `["ou_sre"]`),
		row(KeyAdminUserIDs, `["ou_admin"]`),
	}}
	c := New(src)

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() = %v, want nil", err)
	}

	if !c.IsAdminUser("ou_sre") {
		t.Error("an SRE is not an admin, want an admin")
	}
	if !c.IsAdminUser("ou_admin") {
		t.Error("a listed admin is not an admin")
	}
	if c.IsSREUser("ou_admin") {
		t.Error("an admin-only id is an SRE, want an SRE is not implied")
	}
	if !c.IsSREUser("ou_sre") {
		t.Error("a listed SRE is not an SRE")
	}
	if c.IsAdminUser("ou_stranger") {
		t.Error("a stranger is an admin")
	}
}

// TestRefreshReplacesTheWholeGeneration pins that a refresh is not a merge: an
// id removed from the row loses its role. A merge would make the lists
// impossible to shrink, and revoking authority is the operation that matters
// most.
func TestRefreshReplacesTheWholeGeneration(t *testing.T) {
	src := &fakeSource{rows: []*Row{row(KeyAdminUserIDs, `["ou_admin"]`)}}
	c := New(src)

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() = %v, want nil", err)
	}
	if !c.IsAdminUser("ou_admin") {
		t.Fatal("the first load did not grant the listed admin")
	}

	src.rows = []*Row{row(KeyAdminUserIDs, `[]`)}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() = %v, want nil", err)
	}
	if c.IsAdminUser("ou_admin") {
		t.Error("an emptied list still admits the old admin, want nobody")
	}
}

// TestRefreshKeepsPreviousSnapshotOnError pins the failure direction: a
// database blip must not revoke the deployment's authority. The error is
// reported so the first load can surface it, and kept in Status so /doctor can
// show that the lists are stale.
func TestRefreshKeepsPreviousSnapshotOnError(t *testing.T) {
	src := &fakeSource{rows: []*Row{row(KeyAdminUserIDs, `["ou_admin"]`)}}
	c := New(src)

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() = %v, want nil", err)
	}
	first := c.Status()
	if first.LoadedAt.IsZero() {
		t.Fatal("LoadedAt is zero after a successful load")
	}

	src.err = errors.New("database is down")
	err := c.Refresh(context.Background())
	if err == nil {
		t.Fatal("Refresh() on a failing source = nil, want an error")
	}
	if !errors.Is(err, src.err) {
		t.Errorf("Refresh() error = %v, want it to wrap %v", err, src.err)
	}
	if !c.IsAdminUser("ou_admin") {
		t.Error("a failed refresh revoked the admin, want the previous snapshot kept")
	}
	after := c.Status()
	if after.Err == nil {
		t.Error("Status().Err = nil after a failed refresh, want the failure")
	}
	if !after.LoadedAt.Equal(first.LoadedAt) {
		t.Errorf("Status().LoadedAt = %v after a failed refresh, want the previous %v", after.LoadedAt, first.LoadedAt)
	}

	// A later success clears the recorded failure.
	src.err = nil
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() = %v, want nil", err)
	}
	if c.Status().Err != nil {
		t.Errorf("Status().Err = %v after a successful refresh, want nil", c.Status().Err)
	}
}

// TestDeniesEveryoneWithoutLists pins every way the lists can be empty. An empty
// list means "nobody", not "everybody": a deployment that configured no roles
// has no one who may run the restricted commands, and a cache that was never
// built must not be the exception that grants everyone.
func TestDeniesEveryoneWithoutLists(t *testing.T) {
	empty := New(&fakeSource{})
	if err := empty.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() = %v, want nil", err)
	}

	var nilCache *Cache
	unloaded := New(nil)
	static := NewStatic(nil, nil)

	for _, c := range []*Cache{empty, unloaded, static, nilCache} {
		if c.IsSREUser("ou_sre") {
			t.Error("an empty SRE list admitted a user")
		}
		if c.IsAdminUser("ou_admin") {
			t.Error("an empty admin list admitted a user")
		}
	}
	for _, c := range []*Cache{empty, unloaded, static, nilCache} {
		if c.IsSREUser("") || c.IsAdminUser("") {
			t.Error("an empty id was admitted")
		}
	}
	if got := nilCache.Status(); got.SRE != 0 || got.Admin != 0 {
		t.Errorf("Status() on a nil cache = %+v, want zeroes", got)
	}
	if err := nilCache.Refresh(context.Background()); err == nil {
		t.Error("Refresh() on a nil cache = nil, want an error")
	}
	if err := unloaded.Refresh(context.Background()); !errors.Is(err, ErrNoSource) {
		t.Errorf("Refresh() without a source = %v, want %v", err, ErrNoSource)
	}
}

// TestNewStaticCopiesAndUnions pins the constructor the runtime's fixtures use:
// it must not let a caller keep mutating a map the cache is reading, and it must
// apply the same union rule as a live refresh.
func TestNewStaticCopiesAndUnions(t *testing.T) {
	sre := map[string]struct{}{"ou_sre": {}}
	admin := map[string]struct{}{"ou_admin": {}}
	c := NewStatic(sre, admin)

	sre["ou_later"] = struct{}{}
	admin["ou_other"] = struct{}{}

	if c.IsSREUser("ou_later") {
		t.Error("a mutation of the caller's SRE map reached the cache")
	}
	if c.IsAdminUser("ou_other") {
		t.Error("a mutation of the caller's admin map reached the cache")
	}
	if !c.IsAdminUser("ou_sre") {
		t.Error("NewStatic did not add the SRE list to the admin list")
	}
}

// TestGettersDoNotFetch pins the property the whole cache exists for: the
// message path never touches the database. A getter that loaded on miss would
// make every inbound message a query, and would make the role check fail open
// whenever the load was slow.
func TestGettersDoNotFetch(t *testing.T) {
	src := &fakeSource{rows: []*Row{row(KeyAdminUserIDs, `["ou_admin"]`)}}
	c := New(src)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() = %v, want nil", err)
	}
	if src.calls != 1 {
		t.Fatalf("source calls after one refresh = %d, want 1", src.calls)
	}

	for range 5 {
		c.IsSREUser("ou_admin")
		c.IsAdminUser("ou_admin")
		c.Status()
	}
	if src.calls != 1 {
		t.Errorf("source calls after a refresh plus lookups = %d, want 1", src.calls)
	}
}

// TestRefreshReportsTheFirstLoad covers the operator's only view of this cache
// before an administrator exists: the log line. An empty table is the state a
// deployment is in the moment it is switched over — and the state in which every
// restricted command is refused, /doctor included — so the first load has to be
// reported even when it found nothing. Reporting only on change would be silent
// for exactly that case, because nothing changing into nothing is not a change.
//
// The later loads are the opposite: they are quiet unless the lists moved, since
// they run every refresh.
func TestRefreshReportsTheFirstLoad(t *testing.T) {
	tests := []struct {
		name  string
		rows  []*Row
		want  string
		level slog.Level
	}{
		{
			name:  "an empty table is reported as a warning",
			rows:  nil,
			want:  "no administrators are configured",
			level: slog.LevelWarn,
		},
		{
			name:  "a load that grants somebody is reported as a load",
			rows:  []*Row{row(KeyAdminUserIDs, `["ou_admin"]`)},
			want:  "role lists loaded",
			level: slog.LevelInfo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			c := New(&fakeSource{rows: tt.rows})

			if err := c.Refresh(context.Background()); err != nil {
				t.Fatalf("Refresh() = %v, want nil", err)
			}
			if got := logs.String(); !strings.Contains(got, tt.want) || !strings.Contains(got, tt.level.String()) {
				t.Errorf("first load logged %q, want a %s mentioning %q", got, tt.level, tt.want)
			}

			// A second load of the same rows is not a change, so it says nothing.
			logs.Reset()
			if err := c.Refresh(context.Background()); err != nil {
				t.Fatalf("Refresh() = %v, want nil", err)
			}
			// The snapshot library logs its own debug line on every successful
			// reload; this test pins what *roleset* says, so only its lines
			// count.
			if got := logs.String(); strings.Contains(got, "roleset:") {
				t.Errorf("an unchanged reload logged %q, want nothing about the role lists", got)
			}
		})
	}
}

// captureLogs installs a default logger that keeps what the package logs for the
// duration of one test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// TestStatusReportsSizesOnly pins what /doctor is given: counts and freshness,
// never the identities. A report that listed operators would put them in a chat
// transcript that outlives the question.
func TestStatusReportsSizesOnly(t *testing.T) {
	src := &fakeSource{rows: []*Row{
		row(KeySREUserIDs, `["ou_sre"]`),
		row(KeyAdminUserIDs, `["ou_admin","ou_second"]`),
	}}
	c := New(src)

	if got := c.Status(); got.SRE != 0 || got.Admin != 0 || !got.LoadedAt.IsZero() {
		t.Errorf("Status() before any load = %+v, want zeroes", got)
	}

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() = %v, want nil", err)
	}
	got := c.Status()
	if got.SRE != 1 {
		t.Errorf("Status().SRE = %d, want 1", got.SRE)
	}
	if got.Admin != 3 {
		t.Errorf("Status().Admin = %d, want 3 (two admins plus the SRE)", got.Admin)
	}
	if time.Since(got.LoadedAt) > time.Minute {
		t.Errorf("Status().LoadedAt = %v, want a recent time", got.LoadedAt)
	}
}
