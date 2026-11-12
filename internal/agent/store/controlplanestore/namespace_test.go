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

package controlplanestore

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient/declstore"
)

func TestResolveNamespacePrefersTheObject(t *testing.T) {
	// The store's configured namespace is empty in a deployment that reads every
	// workspace, and the object lives in its workspace's namespace. A write that
	// used the configured one is answered with 404, which is the failure this
	// function exists to prevent.
	list := func() ([]*agentv1alpha1.Agent, error) {
		return []*agentv1alpha1.Agent{
			{ObjectMeta: metav1.ObjectMeta{Name: "fz-public", Namespace: "helper-public"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "onex-x-public", Namespace: "onex"}},
		}, nil
	}

	got, err := resolveNamespace(list, "onex-x-public", "helper-public")
	assert.NoError(t, err)
	assert.Equal(t, "onex", got)
	got, err = resolveNamespace(list, "fz-public", "helper-public")
	assert.NoError(t, err)
	assert.Equal(t, "helper-public", got, "the fallback must not win over what the cache says")
}

func TestResolveNamespaceFallsBackWhenTheCacheMisses(t *testing.T) {
	list := func() ([]*agentv1alpha1.Agent, error) {
		return nil, errors.New("informer cache is not synced")
	}
	got, err := resolveNamespace(list, "onex-x-public", "onex")
	assert.NoError(t, err)
	assert.Equal(t, "onex", got)
	got, err = resolveNamespace(list, "onex-x-public", "")
	assert.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestResolveNamespaceRefusesAmbiguity(t *testing.T) {
	list := func() ([]*agentv1alpha1.Agent, error) {
		return []*agentv1alpha1.Agent{
			{ObjectMeta: metav1.ObjectMeta{Name: "onexai", Namespace: "default"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "onexai", Namespace: "helper-public"}},
		}, nil
	}

	_, err := resolveNamespace(list, "onexai", "helper-public")
	assert.ErrorIs(t, err, declstore.ErrAmbiguousKey,
		"a name in two namespaces must not be resolved to one of them")
}

func TestWriteNamespaceDefaultsToTheDefaultNamespace(t *testing.T) {
	assert.Equal(t, "helper-public", writeNamespace(""), "an unset namespace must not become an empty one")
	assert.Equal(t, "onex", writeNamespace("onex"))
	assert.Equal(t, model.DefaultNamespace, writeNamespace(""))
}

func TestRowNamespaceOrder(t *testing.T) {
	assert.Equal(t, "onex", rowNamespace("onex", ""), "the workspace wins")
	assert.Equal(t, "configured", rowNamespace("", "configured"), "then the configured namespace")
	assert.Equal(t, model.DefaultNamespace, rowNamespace("", ""),
		"and never the literal default, which no reader lists")
}
