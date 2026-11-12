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
	"sync"
	"time"
)

// index is a kind's in-memory picture of what the projection holds.
//
// It replaces a database watermark. Marking every row observed on every pass
// would mean one UPDATE per object per sweep — a million writes every five
// minutes at scale — while this costs one map lookup. The same guarantee comes
// out: a row whose object the current pass did not see is exactly the row to
// delete, and a row written while the pass runs carries a later seenAt and is
// therefore never mistaken for stale.
type index struct {
	mu      sync.Mutex
	entries map[string]indexEntry
}

type indexEntry struct {
	spec   string
	status string
	seenAt time.Time
}

func newIndex() *index {
	return &index{entries: make(map[string]indexEntry)}
}

func indexKey(namespace, name string) string { return namespace + "/" + name }

// seed loads identity and hashes read back from the database at start-up.
func (i *index) seed(entries []CachedEntry) {
	i.mu.Lock()
	defer i.mu.Unlock()

	for _, entry := range entries {
		i.entries[indexKey(entry.Namespace, entry.Name)] = indexEntry{
			spec:   entry.SpecHash,
			status: entry.StatusHash,
		}
	}
}

// get returns the entry for one object.
func (i *index) get(namespace, name string) (indexEntry, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()

	entry, ok := i.entries[indexKey(namespace, name)]
	return entry, ok
}

// put records a row this process has just written.
func (i *index) put(namespace, name, specHash, statusHash string, at time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.entries[indexKey(namespace, name)] = indexEntry{spec: specHash, status: statusHash, seenAt: at}
}

// touch marks an entry as observed by the pass that started at passStart,
// without changing what this process believes the row holds.
func (i *index) touch(namespace, name string, passStart time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()

	key := indexKey(namespace, name)
	entry, ok := i.entries[key]
	if !ok {
		return
	}
	entry.seenAt = passStart
	i.entries[key] = entry
}

// forget drops an entry whose object is gone.
func (i *index) forget(namespace, name string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	delete(i.entries, indexKey(namespace, name))
}

// stale returns the namespaces and names whose entry the pass starting at
// passStart never observed.
func (i *index) stale(passStart time.Time) []Key {
	i.mu.Lock()
	defer i.mu.Unlock()

	var stale []Key
	for key, entry := range i.entries {
		if !entry.seenAt.Before(passStart) {
			continue
		}
		namespace, name := splitIndexKey(key)
		stale = append(stale, Key{Namespace: namespace, Name: name})
	}
	return stale
}

// len reports how many entries this process believes exist.
func (i *index) len() int {
	i.mu.Lock()
	defer i.mu.Unlock()

	return len(i.entries)
}

// splitIndexKey reverses indexKey. A namespace never contains "/", so the first
// separator is the boundary.
func splitIndexKey(key string) (namespace, name string) {
	for idx := 0; idx < len(key); idx++ {
		if key[idx] == '/' {
			return key[:idx], key[idx+1:]
		}
	}
	return "", key
}
