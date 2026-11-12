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

package mirror

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

// ControllerName is the name the projection is enabled by through --controllers.
const ControllerName = "resource-mirror-controller"

// Reconciler projects one kind.
//
// It is level-triggered: the request carries only a key and the object is read
// from the cache, so a replayed or stale event reconciles against current state
// rather than writing a stale row.
type Reconciler struct {
	client.Client

	entry  Kind
	writer RowWriter
	index  *index
}

// Config is everything the projection needs from the controller manager's
// component configuration.
type Config struct {
	// DB is the connection the projection is written through.
	DB *gorm.DB
	// Schema is the PostgreSQL schema holding the projection tables.
	Schema string
	// Kinds narrows the projection; empty means every registered kind.
	Kinds []string
	// BatchSize and FlushInterval bound one write.
	BatchSize     int
	FlushInterval time.Duration
	// SweepInterval is how often the projection is verified against the control
	// plane. It is also how stale a status-only change may be in a List served
	// from the projection.
	SweepInterval time.Duration
	// Namespaces mirrors the manager's cache scope. When non-empty, nothing
	// outside it is loaded or deleted: a narrowed cache must never look like
	// "the other namespaces were deleted".
	Namespaces []string
}

// Add wires the projection into the manager: one reconciler per kind, the shared
// batcher, and the periodic sweep.
//
// A projection that cannot be created is a start-up failure, not a warning: with
// mirror.enabled the operator has declared that reads may depend on these rows,
// and running without them would serve an empty picture instead of an error.
func Add(ctx context.Context, mgr ctrl.Manager, options controller.Options, cfg Config) (bool, error) {
	if cfg.DB == nil {
		return false, fmt.Errorf("mirror: enabled without a database connection")
	}

	kinds, err := Select(cfg.Kinds)
	if err != nil {
		return false, err
	}

	writer, err := NewPostgresWriter(cfg.DB, cfg.Schema)
	if err != nil {
		return false, err
	}
	if err := writer.EnsureSchema(ctx, kinds); err != nil {
		return false, err
	}

	batcher := NewBatcher(writer, cfg.BatchSize, cfg.FlushInterval)
	if err := mgr.Add(&batcherRunnable{batcher: batcher}); err != nil {
		return false, err
	}

	indexes := make(map[string]*index, len(kinds))
	for _, kind := range kinds {
		kindIndex := newIndex()
		// A cluster-scoped kind has no namespace to scope by: its rows carry the
		// empty namespace, so applying the manager's namespace scope to Load
		// would make the first sweep treat every existing row as absent.
		scope := cfg.Namespaces
		if kind.ClusterScoped {
			scope = nil
		}
		entries, err := writer.Load(ctx, kind.Group, kind.Kind, scope)
		if err != nil {
			return false, err
		}
		kindIndex.seed(entries)
		indexes[kind.Kind] = kindIndex

		reconciler := &Reconciler{
			Client: mgr.GetClient(),
			entry:  kind,
			writer: batcher,
			index:  kindIndex,
		}
		if err := reconciler.SetupWithManager(mgr, options); err != nil {
			return false, err
		}
	}

	sweeper := &Sweeper{
		Client:     mgr.GetClient(),
		Writer:     writer,
		Indexes:    indexes,
		Kinds:      kinds,
		Interval:   cfg.SweepInterval,
		Namespaces: cfg.Namespaces,
	}
	if err := mgr.Add(sweeper); err != nil {
		return false, err
	}
	return true, nil
}

// SetupWithManager registers this kind's controller.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(r.entry.New()).
		Named(controllerName(r.entry.Kind)).
		WithOptions(options).
		Complete(r)
}

// Reconcile writes one object, or removes its row when it is gone.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := r.entry.New()
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		if apierrors.IsNotFound(err) {
			r.index.forget(req.Namespace, req.Name)
			return ctrl.Result{}, r.writer.Delete(ctx, []Key{{
				Group:     r.entry.Group,
				Kind:      r.entry.Kind,
				Namespace: req.Namespace,
				Name:      req.Name,
			}})
		}
		return ctrl.Result{}, err
	}

	// A kind that narrows its set (Namespace excludes the control plane's own
	// namespaces) must delete a row it previously projected when the object
	// becomes excluded, not merely stop updating it.
	if r.entry.Exclude != nil && r.entry.Exclude(obj) {
		r.index.forget(req.Namespace, req.Name)
		return ctrl.Result{}, r.writer.Delete(ctx, []Key{{
			Group:     r.entry.Group,
			Kind:      r.entry.Kind,
			Namespace: req.Namespace,
			Name:      req.Name,
		}})
	}

	row, err := Project(r.entry, obj, time.Now())
	if err != nil {
		return ctrl.Result{}, err
	}

	// A status-only change is not written here. Controller status is refreshed on
	// a timer — health probes, resolutions, rosters — and writing each of those
	// would make the projection a permanently hot path. The sweep picks them up.
	if previous, ok := r.index.get(req.Namespace, req.Name); ok && previous.spec == row.SpecHash {
		r.index.touch(req.Namespace, req.Name, time.Now())
		return ctrl.Result{}, nil
	}

	if err := r.writer.Upsert(ctx, []Row{row}); err != nil {
		return ctrl.Result{}, err
	}
	r.index.put(req.Namespace, req.Name, row.SpecHash, row.StatusHash, time.Now())
	return ctrl.Result{}, nil
}

// controllerName is this kind's controller name.
func controllerName(kind string) string {
	return ControllerName + "." + strings.ToLower(kind)
}

// batcherRunnable closes the batcher on shutdown, flushing what it holds.
type batcherRunnable struct {
	batcher *Batcher
}

// Start blocks until shutdown, then flushes. It is not leader-elected: a standby
// replica's batcher holds nothing worth flushing.
func (b *batcherRunnable) Start(ctx context.Context) error {
	<-ctx.Done()
	b.batcher.Close()
	return nil
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (b *batcherRunnable) NeedLeaderElection() bool { return false }
