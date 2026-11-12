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

package model

import "github.com/onexstack/onexai/pkg/apis/agent/sharing"

// Annotation keys carrying the business-line fields that are not part of a
// Kubernetes Namespace's own schema. They are annotations rather than labels so
// arbitrary display text (spaces, Chinese characters) is allowed, and they are
// the contract between the console's CRUD and every reader of a business line:
// the Namespace object is the only source of truth, so both the console and the
// resource store read and write the same keys.
const (
	// AnnotationDisplayName carries the business line's display name.
	AnnotationDisplayName = "onexai.io/display-name"
	// AnnotationDescription carries the business line's description.
	AnnotationDescription = "onexai.io/description"
	// AnnotationOwner carries the business line's owner.
	AnnotationOwner = "onexai.io/owner"
	// AnnotationUpdatedAt carries the last business metadata update in unix
	// milliseconds. A corev1.Namespace has no updatedAt of its own, so this
	// annotation is what the console's 更新时间 column reads.
	AnnotationUpdatedAt = "onexai.io/updated-at"
)

// DefaultNamespace is the business-line namespace every AI resource falls back
// to when it does not declare one. It mirrors opsassist's
// known.DefaultNamespace so records migrated from that system keep resolving
// under the same namespace.
//
// The value lives in pkg/apis/agent/sharing because the sharing rule and the
// namespace default have to agree, and the API layer cannot import this one.
const DefaultNamespace = sharing.DefaultNamespace

// NamespaceM is a business line: the platform-level metadata of a control-plane
// Kubernetes Namespace.
//
// It is a view, not a table. The corev1.Namespace object is the only source of
// truth for identity, labels, annotations and lifecycle; this type is what the
// store and the API layer exchange after mapping the object. There is
// deliberately no GORM registration or TableName: keeping a second row in
// PostgreSQL is what made the old implementation require a reverse syncer and
// left a write that could succeed in one place and fail in the other.
type NamespaceM struct {
	ID          uint64    `json:"id,omitempty"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	Owner       string    `json:"owner"`
	Labels      StringMap `json:"labels"`
	CreatedAt   int64     `json:"created_at"`
	UpdatedAt   int64     `json:"updated_at"`
}

// NormalizeNamespace returns namespace unchanged, or DefaultNamespace when it
// is empty. Every resource record predating the namespace column has an empty
// namespace; normalizing lets those records keep resolving under the default
// namespace with no data migration required.
//
// It is the one function the namespace's readers agree on, which is why it is
// here rather than beside any one type: each type that has the column
// implements GetNamespace in its own file, and all of them call this.
func NormalizeNamespace(namespace string) string {
	if namespace == "" {
		return DefaultNamespace
	}
	return namespace
}
