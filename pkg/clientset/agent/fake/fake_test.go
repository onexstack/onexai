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

package fake

import (
	"context"
	"testing"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// TestUploadAssetEchoesTheRequest pins that the fake answers with what it was
// asked to store. A constant reply would let a test pass on values the server
// would never return — the drift that makes a green suite prove less than it
// looks like it does.
func TestUploadAssetEchoesTheRequest(t *testing.T) {
	c := &Client{}
	req := &v1.CreateAssetRequest{
		Kind:     "image",
		Filename: "shot.png",
		MimeType: "image/jpeg",
		Data:     "AAAA",
		Source:   "lark",
	}
	got, err := c.UploadAsset(context.Background(), req)
	if err != nil {
		t.Fatalf("UploadAsset() error = %v", err)
	}
	if got.GetKind() != "image" || got.GetMimeType() != "image/jpeg" ||
		got.GetName() != "shot.png" || got.GetSource() != "lark" {
		t.Errorf("UploadAsset() = %v, want the request's kind, media type, name and source", got)
	}
	if got.GetAssetId() == "" {
		t.Error("UploadAsset() returned no asset id, leaving the caller nothing to pass on")
	}
}

// TestFetchAssetContentRefusesAnEmptyID pins the fake against the real client,
// which rejects an empty id (agent.go:382). A fake that answered would hide the
// caller bug until production.
func TestFetchAssetContentRefusesAnEmptyID(t *testing.T) {
	c := &Client{}
	if _, _, err := c.FetchAssetContent(context.Background(), ""); err == nil {
		t.Fatal("FetchAssetContent(\"\") = nil error, want a refusal")
	}
}

// TestFetchAssetContentReportsTheConfiguredType pins that the media type is not
// a constant: the real client reports whatever the server sniffed, so a test
// about content types has to be able to move it.
func TestFetchAssetContentReportsTheConfiguredType(t *testing.T) {
	c := &Client{
		AssetContent:  map[string][]byte{"img_1": {1, 2, 3}},
		AssetMimeType: "image/webp",
	}
	data, mimeType, err := c.FetchAssetContent(context.Background(), "img_1")
	if err != nil {
		t.Fatalf("FetchAssetContent() error = %v", err)
	}
	if mimeType != "image/webp" {
		t.Errorf("FetchAssetContent() media type = %q, want image/webp", mimeType)
	}
	if len(data) != 3 {
		t.Errorf("FetchAssetContent() returned %d bytes, want 3", len(data))
	}
}
