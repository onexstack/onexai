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

package handler

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/stretchr/testify/assert"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// TestVerifyLarkWebhookEncryptKeyMode covers the Encrypt Key path: the request
// must carry a valid X-Lark-Signature computed over the raw (still-encrypted)
// body, and the returned body must be the AES-decrypted plaintext event.
func TestVerifyLarkWebhookEncryptKeyMode(t *testing.T) {
	const (
		encryptKey = "test-encrypt-key"
		timestamp  = "1710000000"
		nonce      = "abc123"
	)

	plainEvent := `{"schema":"2.0","header":{"event_id":"evt_1","event_type":"im.message.receive_v1","token":"t"},"event":{}}`
	encrypted := encryptEvent(t, plainEvent, encryptKey)
	encryptedBody, err := json.Marshal(map[string]string{"encrypt": encrypted})
	if err != nil {
		t.Fatalf("marshal encrypted body: %v", err)
	}

	h := &Handler{larkOptions: &customoptions.LarkOptions{EncryptKey: encryptKey}}

	validSig := larkevent.Signature(timestamp, nonce, encryptKey, string(encryptedBody))

	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		signature  string
		wantStatus int
	}{
		{name: "valid signature", signature: validSig, wantStatus: http.StatusOK},
		{name: "tampered signature", signature: "deadbeef", wantStatus: http.StatusUnauthorized},
		{name: "missing signature", signature: "", wantStatus: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/lark/event", nil)
			if tt.signature != "" {
				c.Request.Header.Set(larkevent.EventSignature, tt.signature)
			}
			c.Request.Header.Set(larkevent.EventRequestTimestamp, timestamp)
			c.Request.Header.Set(larkevent.EventRequestNonce, nonce)

			gotBody, err := h.verifyLarkWebhook(c, encryptedBody)
			if tt.wantStatus == http.StatusOK {
				if err != nil {
					t.Fatalf("verifyLarkWebhook returned error for valid request: %v", err)
				}
				if string(gotBody) != plainEvent {
					t.Fatalf("decrypted body mismatch:\n got: %s\nwant: %s", gotBody, plainEvent)
				}
			} else {
				if err == nil {
					t.Fatalf("verifyLarkWebhook succeeded, want error for signature %q", tt.signature)
				}
			}
		})
	}
}

// TestVerifyLarkWebhookVerificationTokenMode covers the plaintext mode where no
// Encrypt Key is configured and the body carries a Verification Token.
func TestVerifyLarkWebhookVerificationTokenMode(t *testing.T) {
	h := &Handler{larkOptions: &customoptions.LarkOptions{VerificationToken: "vt-1"}}

	body := []byte(`{"type":"event_callback","token":"vt-1","event":{}}`)
	wrongBody := []byte(`{"type":"event_callback","token":"vt-2","event":{}}`)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/lark/event", nil)

	if _, err := h.verifyLarkWebhook(c, body); err != nil {
		t.Fatalf("verifyLarkWebhook rejected matching token: %v", err)
	}
	if _, err := h.verifyLarkWebhook(c, wrongBody); err == nil {
		t.Fatal("verifyLarkWebhook accepted mismatched token")
	}
}

// TestVerifyLarkWebhookFailClosed ensures a request is rejected when neither
// Encrypt Key nor Verification Token is configured.
func TestVerifyLarkWebhookFailClosed(t *testing.T) {
	h := &Handler{larkOptions: &customoptions.LarkOptions{}}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/lark/event", nil)

	if _, err := h.verifyLarkWebhook(c, []byte(`{"token":"any"}`)); err == nil {
		t.Fatal("verifyLarkWebhook accepted request with no security configured")
	}
}

// encryptEvent mirrors larkevent.EventDecrypt: it AES-256-CBC encrypts plaintext
// with a SHA256-derived key from secret, prefixes a 16-byte IV, and base64-encodes
// the result. It exists only so the test can produce a real encrypted body;
// production decryption always runs in EventDecrypt.
func encryptEvent(t *testing.T, plaintext, secret string) string {
	t.Helper()

	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}

	iv := []byte("1234567890123456") // fixed IV is fine for a unit test
	padded := pkcs7Pad([]byte(plaintext), aes.BlockSize)

	out := make([]byte, 0, len(iv)+len(padded))
	out = append(out, iv...)
	out = append(out, cbcEncrypt(block, iv, padded)...)
	return base64.StdEncoding.EncodeToString(out)
}

func cbcEncrypt(block cipher.Block, iv, plaintext []byte) []byte {
	dst := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(dst, plaintext)
	return dst
}

func pkcs7Pad(src []byte, blockSize int) []byte {
	n := blockSize - len(src)%blockSize
	return append(src, bytes.Repeat([]byte{byte(n)}, n)...)
}

// TestHandler_WebhookEventHandlers ensures every Lark event type this server
// claims to support is wired to a handler, so a supported event type without a
// handler fails loudly here instead of being silently ignored at runtime.
func TestHandler_WebhookEventHandlers(t *testing.T) {
	h := &Handler{}
	handlers := h.webhookEventHandlers()

	tests := []struct {
		name      string
		eventType string
	}{
		{name: "im_message_receive", eventType: "im.message.receive_v1"},
		{name: "card_action_trigger", eventType: "card.action.trigger"},
		{name: "im_chat_member_bot_added", eventType: "im.chat.member.bot.added_v1"},
		{name: "im_chat_member_bot_deleted", eventType: "im.chat.member.bot.deleted_v1"},
		{name: "im_chat_disbanded", eventType: "im.chat.disbanded_v1"},
		{name: "im_chat_access_event_bot_p2p_chat_entered", eventType: "im.chat.access_event.bot_p2p_chat_entered_v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Contains(t, handlers, tt.eventType)
		})
	}
}

// TestErrorReplyMessage pins the user-facing error contract: a UserError is the
// message the user is meant to read and is sent verbatim, an internal error
// carries a [traceID] prefix so it can be reported back, and a missing trace id
// must not print as a bare "[]". That last case is the one users saw as
// "[]值班组 [go] 不存在" (duty group [go] does not exist).
func TestErrorReplyMessage(t *testing.T) {
	userErr := runtime.NewUserError("值班组 [%s] 不存在", "go")
	internalErr := errors.New("boom")

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{
			name: "user error without trace id",
			ctx:  context.Background(),
			err:  userErr,
			want: "值班组 [go] 不存在",
		},
		{
			name: "user error ignores the trace id",
			ctx:  contextx.WithTraceID(context.Background(), "abc"),
			err:  userErr,
			want: "值班组 [go] 不存在",
		},
		{
			name: "internal error carries the trace id",
			ctx:  contextx.WithTraceID(context.Background(), "abc"),
			err:  internalErr,
			want: "[abc]boom",
		},
		{
			name: "internal error without trace id has no brackets",
			ctx:  context.Background(),
			err:  internalErr,
			want: "boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, errorReplyMessage(tt.ctx, tt.err))
		})
	}
}
