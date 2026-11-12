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
	"context"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/declcrud"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// memoryStore is the long-term memory accessor over the control plane.
type memoryStore struct {
	*Store[model.MemoryM]
}

var _ store.MemoryStore = (*memoryStore)(nil)

// NewMemoryStore builds the long-term memory accessor.
func NewMemoryStore(client *declclient.Client, namespace string) *memoryStore {
	lister := client.Memories()
	list := func() ([]*agentv1alpha1.Memory, error) { return lister.List(labels.Everything()) }

	return &memoryStore{Store: declcrud.NewStore(declcrud.Spec[model.MemoryM, *agentv1alpha1.Memory]{
		Namespace: namespace,
		API: func(namespace string) declcrud.API[*agentv1alpha1.Memory] {
			return client.Clientset.AgentV1alpha1().Memories(namespace)
		},
		GetByKey: getByKey(lister.Memories(namespace).Get, memoryFromCR),
		GetByNamespacedKey: getByNamespacedKey(func(ns, name string) (*agentv1alpha1.Memory, error) {
			return lister.Memories(ns).Get(name)
		}, memoryFromCR),
		List: list,
		FromCR: func(_ context.Context, cr *agentv1alpha1.Memory) (model.MemoryM, error) {
			return memoryFromCR(cr), nil
		},
		ToCR: func(_ context.Context, row *model.MemoryM, cr *agentv1alpha1.Memory) error {
			MemoryToCR(row, cr)
			return nil
		},
		New: func(name, namespace string) *agentv1alpha1.Memory {
			return &agentv1alpha1.Memory{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		},
		Key:    "name",
		KeyOf:  func(row model.MemoryM) string { return row.Name },
		NameOf: func(row model.MemoryM) string { return row.Name },
		Column: memoryColumn,
		Namespaces: rowNamespaces(namespace, list,
			func(row model.MemoryM) string { return rowNamespace(row.Namespace, namespace) }),
	})}
}

func memoryColumn(row model.MemoryM, name string) (string, bool) {
	switch name {
	case "name":
		return row.Name, true
	case "workspace", "namespace":
		return row.Namespace, true
	default:
		return "", false
	}
}

func memoryFromCR(cr *agentv1alpha1.Memory) model.MemoryM {
	owner := ""
	if cr.Spec.Owner != nil {
		owner = cr.Spec.Owner.Name
	}
	return model.MemoryM{
		Name:                  cr.Name,
		UID:                   string(cr.UID),
		Namespace:             cr.Namespace,
		DisplayName:           cr.Spec.DisplayName,
		Description:           cr.Spec.Description,
		Status:                statusOf(cr.Spec.Enabled),
		Enabled:               cr.Spec.Enabled,
		Owner:                 owner,
		EmbeddingModel:        refName(cr.Spec.Embedding.ModelRef),
		EmbeddingDimensions:   cr.Spec.Embedding.Dimensions,
		EmbeddingVersion:      cr.Spec.Embedding.Version,
		ExtractionEnabled:     cr.Spec.Extraction.Enabled,
		ExtractionModel:       refName(cr.Spec.Extraction.ModelRef),
		MaxFactsPerRun:        cr.Spec.Extraction.MaxFactsPerRun,
		TTLDays:               cr.Spec.Retention.TTLDays,
		MaxEntriesPerSubject:  cr.Spec.Retention.MaxEntriesPerSubject,
		RetrievalMode:         cr.Spec.Retrieval.Mode,
		TopK:                  cr.Spec.Retrieval.TopK,
		MinScore:              cr.Spec.Retrieval.MinScore,
		MaxTokens:             cr.Spec.Retrieval.MaxTokens,
		PIIRedactionEnabled:   cr.Spec.Security.PIIRedaction.Enabled,
		PIIStrategy:           cr.Spec.Security.PIIRedaction.Strategy,
		BlockInstructionLike:  cr.Spec.Security.BlockInstructionLike,
		AllowAdminContentRead: cr.Spec.Security.AllowAdminContentRead,
		SubjectMode:           cr.Spec.Scope.SubjectMode,
		Phase:                 cr.Status.Phase,
		Labels:                labelsFrom(cr),
		CreatedAt:             unixMilli(cr.CreationTimestamp),
		UpdatedAt:             unixMilli(cr.CreationTimestamp),
	}
}

// MemoryToCR writes a memory row back into the object.
func MemoryToCR(row *model.MemoryM, cr *agentv1alpha1.Memory) {
	cr.Spec.Enabled = row.Enabled
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.Description
	if row.EmbeddingModel != "" {
		cr.Spec.Embedding.ModelRef = &agentv1alpha1.Reference{Name: row.EmbeddingModel}
	} else {
		cr.Spec.Embedding.ModelRef = nil
	}
	cr.Spec.Embedding.Dimensions = row.EmbeddingDimensions
	cr.Spec.Embedding.Version = row.EmbeddingVersion
	cr.Spec.Extraction.Enabled = row.ExtractionEnabled
	if row.ExtractionModel != "" {
		cr.Spec.Extraction.ModelRef = &agentv1alpha1.Reference{Name: row.ExtractionModel}
	} else {
		cr.Spec.Extraction.ModelRef = nil
	}
	cr.Spec.Extraction.MaxFactsPerRun = row.MaxFactsPerRun
	cr.Spec.Retention.TTLDays = row.TTLDays
	cr.Spec.Retention.MaxEntriesPerSubject = row.MaxEntriesPerSubject
	cr.Spec.Retrieval.Mode = row.RetrievalMode
	cr.Spec.Retrieval.TopK = row.TopK
	cr.Spec.Retrieval.MinScore = row.MinScore
	cr.Spec.Retrieval.MaxTokens = row.MaxTokens
	cr.Spec.Security.PIIRedaction.Enabled = row.PIIRedactionEnabled
	cr.Spec.Security.PIIRedaction.Strategy = row.PIIStrategy
	cr.Spec.Security.BlockInstructionLike = row.BlockInstructionLike
	cr.Spec.Security.AllowAdminContentRead = row.AllowAdminContentRead
	cr.Spec.Scope.SubjectMode = row.SubjectMode
	if row.Owner != "" {
		cr.Spec.Owner = &rbacv1.Subject{Kind: rbacv1.UserKind, Name: row.Owner}
	} else {
		cr.Spec.Owner = nil
	}
	setLabels(cr, row.Labels)
}
