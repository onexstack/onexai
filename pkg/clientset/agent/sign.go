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

package agent

import (
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // a body digest inside an HMAC-signed string, not a security primitive on its own.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"resty.dev/v3"
)

// signer signs one request per the AK/SK contract shared with oai-agent-apiserver:
//
//	bodyMD5 = hex(MD5(body))
//	rawStr  = METHOD + "\n" + PATH + "\n" + bodyMD5 + "\n" + X-TIMESTAMP + "\n" + X-NONCE
//	X-SIGN  = hex(HMAC-SHA256(secretKey, rawStr))
//
// The body must be the exact bytes that go on the wire — see prepare, which
// marshals once and hands the same slice to both the signature and the request.
// Signing a re-serialized body would produce a signature over different bytes
// whenever the encoder orders a map differently, which is the classic way this
// contract is gotten wrong.
type signer struct {
	apiKey    string
	secretKey string
}

func newSigner(apiKey, secretKey string) *signer {
	if apiKey == "" || secretKey == "" {
		return nil
	}
	return &signer{apiKey: apiKey, secretKey: secretKey}
}

// attach adds the four signature headers to r.
func (s *signer) attach(r *resty.Request, method, path string, body []byte) {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := uuid.NewString()

	sum := md5.Sum(body) //nolint:gosec
	rawString := fmt.Sprintf("%s\n%s\n%s\n%s\n%s",
		method, path, hex.EncodeToString(sum[:]), timestamp, nonce)

	mac := hmac.New(sha256.New, []byte(s.secretKey))
	mac.Write([]byte(rawString))

	r.SetHeader("X-API-KEY", s.apiKey).
		SetHeader("X-TIMESTAMP", timestamp).
		SetHeader("X-NONCE", nonce).
		SetHeader("X-SIGN", hex.EncodeToString(mac.Sum(nil)))
}

// prepare builds the request for one call, encoding and signing the body when a
// signer is configured. rawStream means the caller will read the response body
// itself (the SSE endpoints).
//
// The path passed here must be exactly the path the server sees — no query
// string, since the server signs c.Request.URL.Path.
func (c *Client) prepare(ctx context.Context, method, path string, body any, rawStream bool) (*resty.Request, error) {
	r := c.client.R().SetContext(ctx)
	if rawStream {
		r = r.SetResponseDoNotParse(true)
	}
	if c.signer == nil {
		return r.SetBody(body), nil
	}

	raw, err := marshalBody(body)
	if err != nil {
		return nil, err
	}
	c.signer.attach(r, method, path, raw)
	// SetBody of a []byte is written verbatim by resty, which is what keeps the
	// signed bytes and the sent bytes the same.
	r.SetHeader("Content-Type", "application/json")
	return r.SetBody(raw), nil
}

// marshalBody encodes an outgoing request body once, so the bytes signed are
// the bytes sent.
func marshalBody(body any) ([]byte, error) {
	if body == nil {
		return []byte{}, nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("agent: marshal request body: %w", err)
	}
	return raw, nil
}
