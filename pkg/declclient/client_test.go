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

package declclient

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	informers "github.com/onexstack/onexai/pkg/generated/informers/externalversions"
	"github.com/onexstack/onexai/pkg/readmodel"
)

func kindsForTest() []readmodel.KindRef { return readmodel.Kinds }

// TestEveryProjectedKindIsRegistered guards the failure mode that motivated the
// kind table: a lister whose informer was never registered answers "not found"
// forever, and the runtime reads that as a deployment with no objects.
func TestEveryProjectedKindIsRegistered(t *testing.T) {
	client := &Client{Factory: informers.NewSharedInformerFactory(nil, 0)}
	registered := client.buildInformers()

	for _, ref := range readmodel.Kinds {
		if ref.Kind == "BotApp" {
			// Projected for the controller, not read by a runtime: this client
			// deliberately has no BotApp accessor.
			continue
		}
		if _, ok := registered[ref.Kind]; !ok {
			t.Errorf("projected kind %q has no registered informer", ref.Kind)
		}
	}
}

func TestInformerFilterExcludesTheKind(t *testing.T) {
	client := &Client{}
	WithInformerFilter(func(gvk schema.GroupVersionKind) bool {
		return gvk.Kind != "Agent"
	})(client)

	if !client.skippedInformer("agent.onexai.io", "Agent") {
		t.Fatal("the filtered kind was not marked skipped")
	}
	if client.skippedInformer("agent.onexai.io", "Prompt") {
		t.Fatal("an unfiltered kind was marked skipped")
	}

	// The lister accessor must not build the informer it was excluded from:
	// client-go starts an informer added after Start, so touching it would undo
	// the saving.
	called := false
	client.informers = map[string]func() cache.SharedIndexInformer{
		"Agent": func() cache.SharedIndexInformer {
			called = true
			return nil
		},
	}
	indexer := client.indexerFor("Agent")
	if called {
		t.Fatal("the excluded kind's informer was constructed")
	}
	if indexer == nil {
		t.Fatal("the excluded kind has no indexer; the lister accessor would panic")
	}
}

func TestInformerFilterKeepsEveryKindByDefault(t *testing.T) {
	client := &Client{}
	for _, ref := range kindsForTest() {
		if client.skippedInformer(ref.Group, ref.Kind) {
			t.Fatalf("%s was skipped without a filter", ref.Kind)
		}
	}
}
