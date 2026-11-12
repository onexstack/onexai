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

package chatmodel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pngB64(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestOpenAIImageGeneratorRequestsInlineBytes pins the wire request: the format
// is asked for explicitly, because a provider's default answer is a URL that
// expires before the image is served again.
func TestOpenAIImageGeneratorRequestsInlineBytes(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			t.Errorf("path = %q, want /images/generations", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("Authorization = %q", auth)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"` + pngB64(t) + `","revised_prompt":"a cat, wearing a hat"}]}`))
	}))
	defer srv.Close()

	gen := NewOpenAIImageGenerator(srv.URL, "test-key", "gpt-image-1")
	out, err := gen.Generate(context.Background(), ImageRequest{Prompt: "一只猫", Size: "1024x1024"})
	if err != nil {
		t.Fatalf("Generate returned %v", err)
	}

	if got["response_format"] != "b64_json" {
		t.Errorf("response_format = %v, want b64_json", got["response_format"])
	}
	if got["n"] != float64(1) {
		t.Errorf("n = %v, want 1 by default", got["n"])
	}
	if got["model"] != "gpt-image-1" || got["prompt"] != "一只猫" || got["size"] != "1024x1024" {
		t.Errorf("request = %v", got)
	}

	if len(out) != 1 {
		t.Fatalf("got %d images, want 1", len(out))
	}
	// The type is sniffed from the bytes rather than taken from a header, which
	// is what keeps the stored row's type honest.
	if out[0].MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", out[0].MimeType)
	}
	if out[0].RevisedPrompt != "a cat, wearing a hat" {
		t.Errorf("RevisedPrompt = %q", out[0].RevisedPrompt)
	}
	if _, _, err := image.Decode(bytes.NewReader(out[0].Data)); err != nil {
		t.Errorf("the returned bytes are not a decodable image: %v", err)
	}
}

// TestOpenAIImageGeneratorRejectsAURLAnswer pins that a provider which ignored
// the requested format is reported rather than silently producing nothing.
func TestOpenAIImageGeneratorRejectsAURLAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"url":"https://provider.example/tmp/abc.png"}]}`))
	}))
	defer srv.Close()

	gen := NewOpenAIImageGenerator(srv.URL, "", "m")
	_, err := gen.Generate(context.Background(), ImageRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("a URL answer was accepted")
	}
	if !strings.Contains(err.Error(), "URL") {
		t.Errorf("the error does not name what happened: %v", err)
	}
}

// TestOpenAIImageGeneratorSurfacesProviderMessage pins that a refusal carries
// the provider's own words: whether this was a bad prompt, a missing model or a
// quota is only in their answer, and a bare status code sends the operator
// looking in the wrong place.
func TestOpenAIImageGeneratorSurfacesProviderMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"content policy violation"}}`))
	}))
	defer srv.Close()

	gen := NewOpenAIImageGenerator(srv.URL, "", "m")
	_, err := gen.Generate(context.Background(), ImageRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("a 400 was accepted")
	}
	if !strings.Contains(err.Error(), "content policy violation") {
		t.Errorf("the provider's message was dropped: %v", err)
	}
}

func TestOpenAIImageGeneratorRefusesEmptyInput(t *testing.T) {
	gen := NewOpenAIImageGenerator("http://127.0.0.1:1", "", "m")
	if _, err := gen.Generate(context.Background(), ImageRequest{Prompt: "   "}); err == nil {
		t.Error("an empty prompt was accepted")
	}
	// A generator with no model has nothing to send, and says so rather than
	// calling a provider with an empty model name.
	noModel := NewOpenAIImageGenerator("http://127.0.0.1:1", "", "")
	if _, err := noModel.Generate(context.Background(), ImageRequest{Prompt: "x"}); err == nil {
		t.Error("a missing model was accepted")
	}
}
