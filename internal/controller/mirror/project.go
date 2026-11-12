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
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Key identifies one projected row. It is the same tuple the table is keyed by,
// so a delete never has to read the object first.
type Key struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

// Row is one projected object, already flattened.
type Row struct {
	Key

	UID             string
	ResourceVersion int64
	Generation      int64
	CreatedAt       time.Time

	// Enabled, DisplayName and Owner are promoted out of spec because they are
	// the common filters; everything else stays in the jsonb columns.
	Enabled     *bool
	DisplayName *string
	Owner       *string

	Labels      []byte
	Annotations []byte
	Spec        []byte
	Status      []byte

	SpecHash   string
	StatusHash string

	ObservedAt time.Time
}

// KeyOf returns the identity of an object without projecting it.
func KeyOf(entry Kind, obj client.Object) Key {
	return Key{
		Group:     entry.Group,
		Kind:      entry.Kind,
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}
}

// Project flattens a control-plane object into a row.
//
// It is deliberately generic: the object is converted to an unstructured map
// once and the parts the table stores are lifted out of it. A new resource kind
// therefore needs no code here — which is the whole reason the table is one
// generic table rather than fourteen typed ones.
func Project(entry Kind, obj client.Object, now time.Time) (Row, error) {
	unstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return Row{}, fmt.Errorf("mirror: convert %s/%s: %w", entry.Kind, obj.GetName(), err)
	}

	metadata, _ := unstructured["metadata"].(map[string]any)
	spec, _ := unstructured["spec"].(map[string]any)
	status, _ := unstructured["status"].(map[string]any)

	labels, err := canonicalObject(objectMap(metadata, "labels"))
	if err != nil {
		return Row{}, err
	}
	annotations, err := canonicalObject(objectMap(metadata, "annotations"))
	if err != nil {
		return Row{}, err
	}
	specJSON, err := canonicalObject(spec)
	if err != nil {
		return Row{}, err
	}
	statusJSON, err := canonicalObject(status)
	if err != nil {
		return Row{}, err
	}

	row := Row{
		Key:         KeyOf(entry, obj),
		UID:         stringOr(metadata, "uid"),
		Generation:  int64Or(metadata, "generation"),
		CreatedAt:   timeOr(metadata, "creationTimestamp", now),
		Enabled:     boolPtr(spec, "enabled"),
		DisplayName: stringPtr(spec, "displayName"),
		Owner:       ownerOf(spec),
		Labels:      labels,
		Annotations: annotations,
		Spec:        specJSON,
		Status:      statusJSON,
		ObservedAt:  now,
	}
	row.ResourceVersion = parseResourceVersion(stringOr(metadata, "resourceVersion"))
	row.SpecHash = hashOf(specJSON, labels, annotations)
	row.StatusHash = hashOf(statusJSON)
	return row, nil
}

// hashOf hashes the parts of an object the event path compares.
//
// The spec hash deliberately covers labels and annotations: they change the
// object's meaning for selectors and for the admin API, and they change rarely
// enough that folding them in costs nothing. The status hash stands alone
// because status is refreshed on the sweep interval instead of on every event.
func hashOf(parts ...[]byte) string {
	h := sha256.New()
	for _, part := range parts {
		// A length prefix keeps two adjacent parts from hashing the same as
		// their concatenation swapping a byte across the seam.
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalObject marshals one jsonb column.
//
// encoding/json sorts map keys, so the same object always yields the same bytes
// and a hash over them is stable across restarts and replicas. An absent map
// becomes {}, not null: the columns are NOT NULL and the jsonb operators behave
// the same either way, so an empty object is the shape a reader can always
// index into.
func canonicalObject(value map[string]any) ([]byte, error) {
	if len(value) == 0 {
		return []byte("{}"), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("mirror: marshal projection: %w", err)
	}
	return data, nil
}

func objectMap(metadata map[string]any, field string) map[string]any {
	if metadata == nil {
		return nil
	}
	value, _ := metadata[field].(map[string]any)
	return value
}

func stringOr(values map[string]any, field string) string {
	s, _ := values[field].(string)
	return s
}

func stringPtr(values map[string]any, field string) *string {
	s, ok := values[field].(string)
	if !ok {
		return nil
	}
	return &s
}

func boolPtr(values map[string]any, field string) *bool {
	b, ok := values[field].(bool)
	if !ok {
		return nil
	}
	return &b
}

// ownerOf renders spec.owner, which is an rbacv1.Subject, as the principal's
// name. The kind and API group are dropped: every owner in this control plane is
// a user, and a column carrying "User/go.kong" would only make equality
// predicates awkward.
func ownerOf(spec map[string]any) *string {
	owner, _ := spec["owner"].(map[string]any)
	name, ok := owner["name"].(string)
	if !ok || name == "" {
		return nil
	}
	return &name
}

// timeOr parses a metadata timestamp. ToUnstructured renders metav1.Time as its
// RFC3339 JSON form, so a value that does not parse means the object is malformed
// rather than old; the fallback keeps the row writable without inventing a date.
func timeOr(values map[string]any, field string, fallback time.Time) time.Time {
	text, ok := values[field].(string)
	if !ok || text == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return fallback
	}
	return parsed
}

func int64Or(values map[string]any, field string) int64 {
	switch v := values[field].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case int:
		return int64(v)
	default:
		return 0
	}
}

// parseResourceVersion reads the etcd revision carried in metadata. A value that
// does not parse becomes 0, which makes the upsert guard treat the incoming row
// as the oldest possible: it will not overwrite anything it cannot prove is
// newer, and the next sweep repairs it.
func parseResourceVersion(value string) int64 {
	rv, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return rv
}
