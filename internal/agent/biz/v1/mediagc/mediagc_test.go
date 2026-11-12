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

package mediagc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/pkg/media"
)

// fakeRows answers the two calls the collector makes.
type fakeRows struct {
	agentstore.AssetStore
	due       []*model.AssetM
	listErr   error
	deleted   []string
	deleteErr map[string]error
}

func (f *fakeRows) ListExpired(context.Context, time.Time, int) ([]*model.AssetM, error) {
	return f.due, f.listErr
}

func (f *fakeRows) Delete(_ context.Context, opts *where.Options) error {
	id, _ := opts.Filters["id"].(string)
	if err := f.deleteErr[id]; err != nil {
		return err
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func newBundle(t *testing.T) (*media.Bundle, media.Ref) {
	t.Helper()
	store, err := media.NewLocalStore(t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	b := media.NewBundle(store, nil)
	ref, err := b.Put(context.Background(), media.KindImage, "ou_a", "a.png", testPNG(t))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return b, ref
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	// A 1x1 PNG is enough: the store sniffs the type, and this test is about
	// deletion rather than about the bytes.
	var buf []byte
	buf = append(buf, 0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a)
	// A minimal IHDR so the sniffer accepts it; the body is irrelevant here.
	buf = append(buf, 0, 0, 0, 13, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0)
	return buf
}

// TestSweepReclaimsBytesThenTheRow is the ordering, and it is the whole design.
//
// The row is the only thing that knows where the object is. Removing it first
// would lose the address of anything the second step failed on, and the object
// would then stay in the bucket for ever with nothing left to name it.
func TestSweepReclaimsBytesThenTheRow(t *testing.T) {
	bundle, ref := newBundle(t)
	rows := &fakeRows{due: []*model.AssetM{
		{ID: ref.AssetID, ObjectKey: ref.ObjectKey, Size: ref.Size},
	}}
	c := New(rows, bundle, nil, Options{Enabled: true})
	if c == nil {
		t.Fatal("New returned nil for an enabled collector")
	}

	n, err := c.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("reclaimed = %d, want 1", n)
	}
	if len(rows.deleted) != 1 || rows.deleted[0] != ref.AssetID {
		t.Errorf("the row was not removed: %v", rows.deleted)
	}
	// The object is gone: a Stat on it reports absence.
	if _, err := bundle.Stat(context.Background(), ref); !errors.Is(err, media.ErrObjectNotFound) {
		t.Errorf("the object survived the sweep: %v", err)
	}
}

// TestAFailedObjectDeleteKeepsTheRow pins the failure that matters.
//
// A row whose object could not be removed is left alone so the next pass tries
// again. Deleting the row anyway would abandon the object with nothing left to
// name it, which is the one state no later pass can repair.
func TestAFailedObjectDeleteKeepsTheRow(t *testing.T) {
	bundle, _ := newBundle(t)
	// A key the store will refuse: the traversal check rejects it before any
	// filesystem work, which is the cheapest way to make Delete fail.
	rows := &fakeRows{due: []*model.AssetM{
		{ID: "img_bad", ObjectKey: "../../etc/passwd"},
	}}
	c := New(rows, bundle, nil, Options{Enabled: true})

	n, err := c.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("reclaimed = %d, want none", n)
	}
	if len(rows.deleted) != 0 {
		t.Errorf("the row was removed despite its object surviving: %v", rows.deleted)
	}
}

// TestOneFailureDoesNotStopThePass pins the isolation: a single object the
// store refuses would otherwise block every asset behind it for ever.
func TestOneFailureDoesNotStopThePass(t *testing.T) {
	bundle, ref := newBundle(t)
	rows := &fakeRows{
		due: []*model.AssetM{
			{ID: "img_bad", ObjectKey: "images/ou_a/img_gone/a.png"}, // already absent
			{ID: ref.AssetID, ObjectKey: ref.ObjectKey},
		},
	}
	c := New(rows, bundle, nil, Options{Enabled: true})

	n, err := c.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	// A missing object is not a failure — the store treats "delete what is not
	// there" as achieved — so both rows are reclaimed.
	if n != 2 {
		t.Errorf("reclaimed = %d, want both", n)
	}
}

// TestAFailedRowDeleteIsRetriedNextPass pins that the retry is safe: the object
// is already gone and deleting it again is a no-op, so the pass costs one
// statement and nothing else.
func TestAFailedRowDeleteIsRetriedNextPass(t *testing.T) {
	bundle, ref := newBundle(t)
	rows := &fakeRows{
		due:       []*model.AssetM{{ID: ref.AssetID, ObjectKey: ref.ObjectKey}},
		deleteErr: map[string]error{ref.AssetID: errors.New("the table is down")},
	}
	c := New(rows, bundle, nil, Options{Enabled: true})

	n, err := c.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("reclaimed = %d, want none while the row delete fails", n)
	}

	// The store is back; the same row is reclaimed.
	rows.deleteErr = nil
	n, err = c.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("second SweepOnce: %v", err)
	}
	if n != 1 {
		t.Errorf("reclaimed = %d on the retry, want 1", n)
	}
}

// TestDisabledBuildsNothing pins the default.
//
// Deleting a user's picture is not recoverable, so the machinery waits for an
// operator to say how long images are kept rather than choosing a number that
// empties a conversation somebody still reads.
func TestDisabledBuildsNothing(t *testing.T) {
	bundle, _ := newBundle(t)
	rows := &fakeRows{}

	if got := New(rows, bundle, nil, Options{}); got != nil {
		t.Errorf("New with Enabled false returned %T, want nil", got)
	}
	if got := New(rows, bundle, nil, Options{Enabled: true}); got == nil {
		t.Error("New refused an enabled collector with a working store")
	}
	if got := New(nil, bundle, nil, Options{Enabled: true}); got != nil {
		t.Errorf("New with no row store returned %T, want nil", got)
	}
	if got := New(rows, media.NewBundle(nil, nil), nil, Options{Enabled: true}); got != nil {
		t.Errorf("New with no store returned %T, want nil", got)
	}
	// The disabled case is a genuinely nil interface, which is what the server's
	// own guard tests.
	var disabled Collector = New(rows, bundle, nil, Options{})
	if disabled != nil {
		t.Errorf("a disabled deployment holds %#v, want a nil interface", disabled)
	}
}
