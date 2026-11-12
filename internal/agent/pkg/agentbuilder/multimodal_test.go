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

package agentbuilder

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk/filesystem"
)

// TestMultiModalRead pins the mapping from a sandbox file to what a vision
// model receives, and the two cases where it must stay text: a file that is not
// an image or PDF, and one too large to send.
func TestMultiModalRead(t *testing.T) {
	dir := t.TempDir()
	backend := newTestBackend(t, dir)
	ctx := context.Background()

	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	png := []byte("\x89PNG\r\n\x1a\n-not really a png")
	pngPath := write("diagram.png", png)

	got, err := backend.MultiModalRead(ctx, &filesystem.MultiModalReadRequest{
		ReadRequest: filesystem.ReadRequest{FilePath: pngPath},
	})
	if err != nil {
		t.Fatalf("MultiModalRead(image): %v", err)
	}
	if len(got.Parts) != 1 {
		t.Fatalf("image returned %d parts, want 1", len(got.Parts))
	}
	if got.Parts[0].Type != filesystem.FileContentPartTypeImage {
		t.Errorf("part type = %q, want image", got.Parts[0].Type)
	}
	if got.Parts[0].MIMEType != "image/png" {
		t.Errorf("mime = %q, want image/png", got.Parts[0].MIMEType)
	}
	if !bytes.Equal(got.Parts[0].Data, png) {
		t.Error("part data does not match the file")
	}

	pdfPath := write("manual.pdf", []byte("%PDF-1.7"))
	got, err = backend.MultiModalRead(ctx, &filesystem.MultiModalReadRequest{
		ReadRequest: filesystem.ReadRequest{FilePath: pdfPath},
	})
	if err != nil {
		t.Fatalf("MultiModalRead(pdf): %v", err)
	}
	if len(got.Parts) != 1 || got.Parts[0].Type != filesystem.FileContentPartTypePDF {
		t.Fatalf("pdf parts = %+v, want one pdf part", got.Parts)
	}

	// A text file takes the plain reader, so the enhanced tool stays a drop-in
	// for the tool it replaced.
	textPath := write("notes.md", []byte("# 约定\n"))
	got, err = backend.MultiModalRead(ctx, &filesystem.MultiModalReadRequest{
		ReadRequest: filesystem.ReadRequest{FilePath: textPath},
	})
	if err != nil {
		t.Fatalf("MultiModalRead(text): %v", err)
	}
	if len(got.Parts) != 0 {
		t.Fatalf("text file returned %d parts, want none", len(got.Parts))
	}
	if got.FileContent == nil || !strings.Contains(got.FileContent.Content, "约定") {
		t.Fatalf("text file content = %+v, want the file's text", got.FileContent)
	}

	// Too large to send: a text answer the model can act on, not an error that
	// ends the call.
	bigPath := write("big.png", make([]byte, maxMultiModalBytes+1))
	got, err = backend.MultiModalRead(ctx, &filesystem.MultiModalReadRequest{
		ReadRequest: filesystem.ReadRequest{FilePath: bigPath},
	})
	if err != nil {
		t.Fatalf("MultiModalRead(oversize): %v", err)
	}
	if len(got.Parts) != 0 {
		t.Fatalf("oversize file returned %d parts, want none", len(got.Parts))
	}
	if got.FileContent == nil || !strings.Contains(got.FileContent.Content, "上限") {
		t.Fatalf("oversize content = %+v, want a size notice", got.FileContent)
	}
}

// TestMultiModalReadStaysInTheSandbox pins that the new read path goes through
// the same path check as Read: a backend that implements another method must not
// become a way out of the skills directory.
func TestMultiModalReadStaysInTheSandbox(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := filepath.Join(dir, "secret.png")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	backend := newTestBackend(t, skills)

	got, err := backend.MultiModalRead(context.Background(), &filesystem.MultiModalReadRequest{
		ReadRequest: filesystem.ReadRequest{FilePath: outside},
	})
	if err != nil {
		t.Fatalf("MultiModalRead(outside): %v", err)
	}
	if len(got.Parts) != 0 {
		t.Fatal("a file outside the sandbox was returned as model input")
	}
	if got.FileContent == nil || !strings.Contains(got.FileContent.Content, blockedPrefix) {
		t.Fatalf("outside content = %+v, want a refusal", got.FileContent)
	}
}
