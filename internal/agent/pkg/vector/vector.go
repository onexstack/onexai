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

// Package vector encodes float32 embeddings for storage.
//
// The binary form is portable: it is what SQLite and MySQL keep in a blob and
// what PostgreSQL keeps in the bytea column, so retrieval and MMR work on every
// dialect. The text form is the pgvector literal used by the optional
// embedding_vec column and its HNSW index.
package vector

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"
)

// Encode packs a vector into little-endian float32 bytes. nil stays nil so a
// cleared embedding is written as SQL NULL, not as a zero-length blob.
func Encode(values []float32) []byte {
	if len(values) == 0 {
		return nil
	}
	out := make([]byte, 4*len(values))
	for i, value := range values {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(value))
	}
	return out
}

// Decode unpacks float32 bytes. A blob whose length is not a multiple of four
// is corrupt, and nil is the honest answer.
func Decode(raw []byte) []float32 {
	if len(raw) == 0 || len(raw)%4 != 0 {
		return nil
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out
}

// Literal renders a vector as the pgvector text form, e.g. "[0.1,0.2]".
func Literal(values []float32) string {
	if len(values) == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(len(values) * 12)
	b.WriteByte('[')
	for i, value := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
