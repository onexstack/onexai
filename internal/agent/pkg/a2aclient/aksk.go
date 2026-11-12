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

package a2aclient

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // MD5 is a body digest inside a signed string, not a standalone primitive.
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// NewAKSKTransport signs every outbound request with the onexai service
// credential. It is the only way to call another onexai A2A surface, whose card
// advertises the aksk extension rather than a bearer token.
func NewAKSKTransport(base http.RoundTripper, accessKey, secretKey string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if accessKey == "" || secretKey == "" {
		return base
	}
	return &akskTransport{base: base, accessKey: accessKey, secretKey: secretKey}
}

type akskTransport struct {
	base      http.RoundTripper
	accessKey string
	secretKey string
}

func (t *akskTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	var body []byte
	if req.Body != nil {
		data, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		body = data
		clone.Body = io.NopCloser(bytes.NewReader(data))
		clone.ContentLength = int64(len(data))
	}

	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := uuid.NewString()
	sum := md5.Sum(body) //nolint:gosec // see the package note.
	signing := req.Method + "\n" + req.URL.Path + "\n" + hex.EncodeToString(sum[:]) + "\n" + timestamp + "\n" + nonce
	mac := hmac.New(sha256.New, []byte(t.secretKey))
	mac.Write([]byte(signing))
	clone.Header.Set("X-API-KEY", t.accessKey)
	clone.Header.Set("X-TIMESTAMP", timestamp)
	clone.Header.Set("X-NONCE", nonce)
	clone.Header.Set("X-SIGN", hex.EncodeToString(mac.Sum(nil)))
	return t.base.RoundTrip(clone)
}
