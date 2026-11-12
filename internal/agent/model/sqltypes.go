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

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// StringList is a []string carried in a text column as a JSON array.
//
// # Why a type rather than a plain []string with a serializer
//
// The columns these fields map to are `text` and already hold data, and the
// encoding they hold is specific: a compact JSON array such as `["a","b"]`, and
// — the part a serializer gets wrong — an **empty string** for an empty list.
// GORM's `serializer:json` writes `null` for a nil slice and `[]` for an empty
// one, so pointing it at these columns would leave every existing empty row
// unreadable (`unexpected end of JSON input`) and quietly rewrite the rest.
//
// Value and Scan reproduce the existing encoding byte for byte, so the type can
// be dropped onto a model with no data migration. That is the whole point of
// it; the list was previously encoded and decoded by hand at the store
// boundary, in two functions every reader and writer had to remember to call.
type StringList []string

// Value encodes the list for storage: "" when empty, otherwise a JSON array.
func (l StringList) Value() (driver.Value, error) {
	if len(l) == 0 {
		return "", nil
	}
	b, err := json.Marshal([]string(l))
	if err != nil {
		return nil, fmt.Errorf("encoding string list: %w", err)
	}
	return string(b), nil
}

// Scan decodes a column value, tolerating both the empty string and NULL.
//
// A value that is neither is read as an empty list rather than as an error: the
// column predates this type, its contents are not guaranteed to be JSON, and a
// single unparsable row should not fail the query that loaded it — which is what
// the hand-written decoder it replaces did.
func (l *StringList) Scan(src any) error {
	*l = nil
	s, err := scanText(src, "string list")
	if err != nil || s == "" {
		return err
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	*l = out
	return nil
}

// StringMap is a map[string]string carried in a text column as a JSON object,
// under the same encoding rules as StringList: "" when empty, JSON otherwise.
type StringMap map[string]string

// Value encodes the map for storage: "" when empty, otherwise a JSON object.
func (m StringMap) Value() (driver.Value, error) {
	if len(m) == 0 {
		return "", nil
	}
	b, err := json.Marshal(map[string]string(m))
	if err != nil {
		return nil, fmt.Errorf("encoding string map: %w", err)
	}
	return string(b), nil
}

// Scan decodes a column value, tolerating both the empty string and NULL, and
// reading anything that will not parse as empty for the reason StringList.Scan
// gives.
func (m *StringMap) Scan(src any) error {
	*m = nil
	s, err := scanText(src, "string map")
	if err != nil || s == "" {
		return err
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	*m = out
	return nil
}

// scanText normalises the two shapes a text column arrives in, and refuses
// everything else. A column this repository owns is a string or bytes or NULL;
// anything else means the type was put on a column that is not text.
func scanText(src any, what string) (string, error) {
	switch v := src.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	default:
		return "", fmt.Errorf("decoding %s: unsupported column type %T", what, src)
	}
}
