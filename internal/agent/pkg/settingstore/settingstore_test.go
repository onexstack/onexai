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

package settingstore

import (
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

type fakeSource map[string]string

func (f fakeSource) Lookup(key string) (string, bool) {
	value, ok := f[key]
	return value, ok
}

func TestNew_NoSourceIsNil(t *testing.T) {
	if store := New(nil); store != nil {
		t.Error("a store with nowhere to read from must be nil")
	}
	// Every accessor has to tolerate that: a deployment with no settings reads
	// the caller's fallback, it does not crash.
	var missing *Store
	if got := missing.GetString("anything"); got != "" {
		t.Errorf("GetString on a nil store = %q, want empty", got)
	}
	if got := missing.GetInt("anything", 7); got != 7 {
		t.Errorf("GetInt on a nil store = %d, want the fallback 7", got)
	}
}

func TestTypedAccessors(t *testing.T) {
	store := New(fakeSource{
		"n":    "42",
		"b":    "true",
		"d":    "90s",
		"list": " a , b ,, c ",
		"bad":  "not-a-number",
	})

	if got := store.GetInt("n", 1); got != 42 {
		t.Errorf("GetInt = %d, want 42", got)
	}
	if got := store.GetBool("b", false); !got {
		t.Error("GetBool = false, want true")
	}
	if got := store.GetDuration("d", time.Second); got != 90*time.Second {
		t.Errorf("GetDuration = %v, want 90s", got)
	}
	if got := store.GetStringSlice("list"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("GetStringSlice = %v, want [a b c]", got)
	}

	// A malformed value falls back rather than failing: nothing here is worth
	// failing a request over.
	if got := store.GetInt("bad", 7); got != 7 {
		t.Errorf("GetInt on a malformed value = %d, want the fallback 7", got)
	}
	if got := store.GetInt("absent", 7); got != 7 {
		t.Errorf("GetInt on an unset key = %d, want the fallback 7", got)
	}
	if got := store.GetStringSlice("absent"); got != nil {
		t.Errorf("GetStringSlice on an unset key = %v, want nil", got)
	}
}

// newConfigMapLister builds the informer index a ConfigMap lister reads, which
// is what the API server-side watch would populate.
func newConfigMapLister(t *testing.T) (*cache.Indexer, corelisters.ConfigMapLister) {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
	})
	return &indexer, corelisters.NewConfigMapLister(indexer)
}

func configMap(namespace, name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Data:       data,
	}
}

func TestConfigMapSource_ReadsTheDataOfOneConfigMap(t *testing.T) {
	indexer, lister := newConfigMapLister(t)
	source := NewConfigMapSource(lister.ConfigMaps("default"), "oai-agent-settings")

	if _, ok := source.Lookup(KeySystemPrompt); ok {
		t.Error("a ConfigMap that does not exist yet must read as unset")
	}

	if err := (*indexer).Add(configMap("default", "oai-agent-settings", map[string]string{
		KeySystemPrompt: "you are on call",
	})); err != nil {
		t.Fatalf("add the ConfigMap to the index: %v", err)
	}

	value, ok := source.Lookup(KeySystemPrompt)
	if !ok || value != "you are on call" {
		t.Errorf("Lookup = (%q, %v), want (\"you are on call\", true)", value, ok)
	}
	if _, ok := source.Lookup("absent"); ok {
		t.Error("an absent key must read as unset")
	}

	// An edit is visible on the next lookup with no refresh timer in between:
	// the lister is the cache.
	if err := (*indexer).Update(configMap("default", "oai-agent-settings", map[string]string{
		KeySystemPrompt: "you are off call",
	})); err != nil {
		t.Fatalf("update the ConfigMap in the index: %v", err)
	}
	if got := New(source).GetString(KeySystemPrompt); got != "you are off call" {
		t.Errorf("GetString after the edit = %q, want the new value", got)
	}
}

func TestConfigMapSource_OtherNamespacesAndNamesAreIgnored(t *testing.T) {
	indexer, lister := newConfigMapLister(t)
	for _, cm := range []*corev1.ConfigMap{
		configMap("other", "oai-agent-settings", map[string]string{KeySystemPrompt: "another namespace"}),
		configMap("default", "unrelated", map[string]string{KeySystemPrompt: "another name"}),
	} {
		if err := (*indexer).Add(cm); err != nil {
			t.Fatalf("add %s/%s: %v", cm.Namespace, cm.Name, err)
		}
	}

	source := NewConfigMapSource(lister.ConfigMaps("default"), "oai-agent-settings")
	if _, ok := source.Lookup(KeySystemPrompt); ok {
		t.Error("only the declared ConfigMap in the declared namespace may be read")
	}
}

func TestConfigMapSource_NoNameIsUnset(t *testing.T) {
	_, lister := newConfigMapLister(t)
	if _, ok := NewConfigMapSource(lister.ConfigMaps("default"), "").Lookup(KeySystemPrompt); ok {
		t.Error("a source built without a ConfigMap name must read as unset")
	}
}
