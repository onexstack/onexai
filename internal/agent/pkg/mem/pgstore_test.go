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

package mem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/pkg/media"
)

func TestSessionImagesRoundTrip(t *testing.T) {
	images := []media.Ref{{AssetID: "asset-1"}, {AssetID: "asset-2"}}
	encoded, err := encodeImages(images)
	require.NoError(t, err)
	decoded, err := decodeImages(encoded)
	require.NoError(t, err)
	assert.Equal(t, images, decoded)
	assert.Nil(t, mustDecode(t, ""))
}

func mustDecode(t *testing.T, raw string) []media.Ref {
	t.Helper()
	out, err := decodeImages(raw)
	require.NoError(t, err)
	return out
}
