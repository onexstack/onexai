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

package options

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/onexstack/onexai/pkg/readmodel"
)

// Read backends, chosen per resource kind.
//
// The three are not three code paths but three answers to "how much memory may
// this kind cost, and how consistent must a read be". The default keeps today's
// behaviour exactly, which is what makes switching a kind over a deliberate act.
const (
	// BackendControlPlane reads from the informer cache. List is O(N) per
	// request and Get is O(1); memory grows with the kind.
	BackendControlPlane = "controlplane"
	// BackendMirrorList answers List from the PostgreSQL projection and keeps the
	// informer for Get. List becomes O(page); reads stay strongly consistent.
	BackendMirrorList = "mirror-list"
	// BackendMirrorFull also drops the informer, so Get goes to the API server
	// behind a bounded LRU. Memory stops tracking the object count.
	BackendMirrorFull = "mirror-full"
)

// StoreOptions selects and configures the AI-resource store backend.
//
// The declarative resources (agents, skills, MCP servers, knowledge bases,
// prompts, models, webhooks, runtime profiles and AK/SK secrets) are served by
// the control plane; Source only controls the SQL tables that stayed behind
// (audit, assets, access keys). On-call groups are a chat-owned SQL resource and
// are not selected here.
type StoreOptions struct {
	// Source selects the backend: "sql" | "" (disabled).
	// "mysql" is accepted as a deprecated alias of "sql": the backend is a GORM
	// store that follows the coredb dialect, so naming it after one of the two
	// databases it can run on was misleading.
	Source string `json:"source" mapstructure:"source"`
	// RefreshInterval is both the freshness window and the warm-up period of
	// the resource snapshots.
	RefreshInterval time.Duration `json:"refresh-interval" mapstructure:"refresh-interval"`

	// ReadBackend is the default read backend for every kind.
	ReadBackend string `json:"read-backend" mapstructure:"read-backend"`
	// ReadBackends overrides ReadBackend per Kind. Only YAML can express this:
	// a flag cannot carry a map.
	ReadBackends map[string]string `json:"read-backends" mapstructure:"read-backends"`
	// Mirror configures the PostgreSQL read model.
	Mirror StoreMirrorOptions `json:"mirror" mapstructure:"mirror"`
	// GetCacheSize bounds the per-process LRU that mirror-full Get uses.
	GetCacheSize int `json:"get-cache-size" mapstructure:"get-cache-size"`
}

// StoreMirrorOptions configures the PostgreSQL read model.
type StoreMirrorOptions struct {
	// Schema is the PostgreSQL schema the projection lives in.
	Schema string `json:"schema" mapstructure:"schema"`
	// MaxLag is how far behind the projection may be before List falls back to
	// the informer cache.
	MaxLag time.Duration `json:"max-lag" mapstructure:"max-lag"`
	// CountMode is "exact" or "estimate"; estimate reads the projection's row
	// count instead of running count(*) for an unfiltered List.
	CountMode string `json:"count-mode" mapstructure:"count-mode"`
}

// NewStoreOptions creates StoreOptions with default values.
func NewStoreOptions() *StoreOptions {
	return &StoreOptions{
		Source:          "",
		RefreshInterval: 5 * time.Minute,
		ReadBackend:     BackendControlPlane,
		Mirror: StoreMirrorOptions{
			Schema:    readmodel.DefaultSchema,
			MaxLag:    10 * time.Minute,
			CountMode: "exact",
		},
		GetCacheSize: 10000,
	}
}

// UsesSQL reports whether the resource store is backed by the GORM database,
// regardless of which dialect that database happens to speak.
func (o *StoreOptions) UsesSQL() bool {
	return o != nil && (o.Source == "sql" || o.Source == "mysql")
}

// BackendFor returns the read backend of one kind.
func (o *StoreOptions) BackendFor(kind string) string {
	if o == nil {
		return BackendControlPlane
	}
	if backend := o.backendOverride(kind); backend != "" {
		return backend
	}
	if o.ReadBackend == "" {
		return BackendControlPlane
	}
	return o.ReadBackend
}

// backendOverride returns the per-kind selection, matching the kind name
// case-insensitively. The configuration loader lower-cases map keys, so a YAML
// entry spelled `Namespace` arrives as `namespace`; comparing exactly would
// silently leave the kind on the default backend.
func (o *StoreOptions) backendOverride(kind string) string {
	if backend, ok := o.ReadBackends[kind]; ok && backend != "" {
		return backend
	}
	for name, backend := range o.ReadBackends {
		if backend != "" && strings.EqualFold(name, kind) {
			return backend
		}
	}
	return ""
}

// isKnownKindFold reports whether name is a projected kind, ignoring case for
// the same reason backendOverride does.
func isKnownKindFold(name string) bool {
	if readmodel.IsKnownKind(name) {
		return true
	}
	for _, known := range readmodel.KindNames() {
		if strings.EqualFold(known, name) {
			return true
		}
	}
	return false
}

// UsesMirror reports whether any kind reads from the projection.
func (o *StoreOptions) UsesMirror() bool {
	if o == nil {
		return false
	}
	if o.ReadBackend == BackendMirrorList || o.ReadBackend == BackendMirrorFull {
		return true
	}
	for _, backend := range o.ReadBackends {
		if backend == BackendMirrorList || backend == BackendMirrorFull {
			return true
		}
	}
	return false
}

// MirroredKinds returns the kinds whose List is answered from the projection.
func (o *StoreOptions) MirroredKinds() []string {
	if o == nil {
		return nil
	}
	kinds := make([]string, 0, len(readmodel.Kinds))
	for _, kind := range readmodel.Kinds {
		switch o.BackendFor(kind.Kind) {
		case BackendMirrorList, BackendMirrorFull:
			kinds = append(kinds, kind.Kind)
		}
	}
	return kinds
}

// InformerlessKinds returns the kinds whose informer must not be registered.
func (o *StoreOptions) InformerlessKinds() []string {
	if o == nil {
		return nil
	}
	kinds := make([]string, 0, len(readmodel.Kinds))
	for _, kind := range readmodel.Kinds {
		if o.BackendFor(kind.Kind) == BackendMirrorFull {
			kinds = append(kinds, kind.Kind)
		}
	}
	return kinds
}

// InformerFilter reports whether the informer for kind should run in this
// process.
//
// It is the other half of mirror-full: dropping Get's dependence on the cache
// saves nothing unless the cache is never filled, and client-go starts an
// informer added after Start — so the exclusion has to happen at construction.
func (o *StoreOptions) InformerFilter() func(gvk schema.GroupVersionKind) bool {
	skip := make(map[string]struct{})
	for _, kind := range o.InformerlessKinds() {
		skip[kind] = struct{}{}
	}
	return func(gvk schema.GroupVersionKind) bool {
		_, excluded := skip[gvk.Kind]
		return !excluded
	}
}

// Validate checks the StoreOptions fields.
func (o *StoreOptions) Validate() []error {
	if o == nil {
		return nil
	}

	var errs []error
	switch o.Source {
	case "", "sql", "mysql":
	default:
		errs = append(errs, fmt.Errorf("store.source %q is invalid (want sql | empty)", o.Source))
	}

	switch o.ReadBackend {
	case "", BackendControlPlane, BackendMirrorList, BackendMirrorFull:
	default:
		errs = append(errs, fmt.Errorf("store.read-backend %q is invalid (want %s | %s | %s)",
			o.ReadBackend, BackendControlPlane, BackendMirrorList, BackendMirrorFull))
	}

	for kind, backend := range o.ReadBackends {
		if !isKnownKindFold(kind) {
			errs = append(errs, fmt.Errorf("store.read-backends names unknown kind %q (known: %v)",
				kind, readmodel.KindNames()))
			continue
		}
		switch backend {
		case BackendControlPlane, BackendMirrorList, BackendMirrorFull:
		default:
			errs = append(errs, fmt.Errorf("store.read-backends[%s] %q is invalid (want %s | %s | %s)",
				kind, backend, BackendControlPlane, BackendMirrorList, BackendMirrorFull))
		}
	}

	switch o.Mirror.CountMode {
	case "", "exact", "estimate":
	default:
		errs = append(errs, fmt.Errorf("store.mirror.count-mode %q is invalid (want exact | estimate)", o.Mirror.CountMode))
	}
	if o.Mirror.MaxLag < 0 {
		errs = append(errs, fmt.Errorf("store.mirror.max-lag must not be negative"))
	}
	if o.GetCacheSize < 0 {
		errs = append(errs, fmt.Errorf("store.get-cache-size must not be negative"))
	}

	return errs
}

// AddFlags binds StoreOptions to command-line flags.
func (o *StoreOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.Source, fullPrefix+".source", o.Source, ""+
		"AI-resource store backend: sql | (empty = disabled).")
	fs.DurationVar(&o.RefreshInterval, fullPrefix+".refresh-interval", o.RefreshInterval, ""+
		"AI-resource snapshot freshness window and warm-up period (<=0 disables caching).")
	fs.StringVar(&o.ReadBackend, fullPrefix+".read-backend", o.ReadBackend, ""+
		"Default read backend for resource kinds: controlplane | mirror-list | mirror-full.")
	fs.StringVar(&o.Mirror.Schema, fullPrefix+".mirror.schema", o.Mirror.Schema, ""+
		"PostgreSQL schema holding the resource projection.")
	fs.DurationVar(&o.Mirror.MaxLag, fullPrefix+".mirror.max-lag", o.Mirror.MaxLag, ""+
		"How far behind the projection may be before List falls back to the informer cache.")
	fs.StringVar(&o.Mirror.CountMode, fullPrefix+".mirror.count-mode", o.Mirror.CountMode, ""+
		"List total: exact runs count(*) | estimate reads the projection's row count.")
	fs.IntVar(&o.GetCacheSize, fullPrefix+".get-cache-size", o.GetCacheSize, ""+
		"Entries cached for Get when a kind reads with mirror-full.")
}
