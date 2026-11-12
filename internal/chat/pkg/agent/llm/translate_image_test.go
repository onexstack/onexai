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

package llm

import (
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	llmv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// TestTranslateImageEvent pins both halves of the image event crossing the
// wire: the type is recognised, and the references survive the trip.
//
// A dropped type here is not an error anywhere — the translation reports
// "not an event" and the stream simply continues — so a picture that stopped
// arriving would look exactly like a run that drew nothing. That is what this
// test is for.
func TestTranslateImageEvent(t *testing.T) {
	typ, ok := TranslateEventType(llmv1.AgentEventType_agentEventTypeImage)
	if !ok {
		t.Fatal("the image event type is not recognised")
	}
	if typ != agent.EventImage {
		t.Fatalf("translated to %v, want EventImage", typ)
	}

	ev, ok := TranslateEvent(&llmv1.AgentStreamEvent{
		Type: llmv1.AgentEventType_agentEventTypeImage,
		Images: []*llmv1.ImageRef{{
			AssetId:  "img_a",
			Url:      "/media/images/u/img_a/a.png",
			MimeType: "image/png",
			Width:    1024,
			Height:   1024,
			Source:   "generated",
		}},
	})
	if !ok {
		t.Fatal("the event did not translate")
	}
	if ev.Type != agent.EventImage {
		t.Fatalf("Type = %v, want EventImage", ev.Type)
	}
	if len(ev.Images) != 1 {
		t.Fatalf("Images = %+v, want one", ev.Images)
	}
	got := ev.Images[0]
	if got.AssetID != "img_a" || got.MimeType != "image/png" || got.Width != 1024 || got.Height != 1024 {
		t.Errorf("the reference lost fields in translation: %+v", got)
	}
	if got.Source != "generated" {
		t.Errorf("Source = %q, want generated", got.Source)
	}
}

// TestTranslateIgnoresUnknownEventType pins the compatibility rule the proto
// relies on: a value this build does not know is dropped rather than mistaken
// for one of the types it does.
func TestTranslateIgnoresUnknownEventType(t *testing.T) {
	if _, ok := TranslateEventType(llmv1.AgentEventType(99)); ok {
		t.Error("an unknown event type was translated instead of ignored")
	}
}

// TestTranslateImageSkipsNilEntries pins that a malformed stream cannot panic a
// renderer: the card walks the list, and a nil element would be a panic in the
// middle of an otherwise fine run.
func TestTranslateImageSkipsNilEntries(t *testing.T) {
	ev, ok := TranslateEvent(&llmv1.AgentStreamEvent{
		Type:   llmv1.AgentEventType_agentEventTypeImage,
		Images: []*llmv1.ImageRef{nil, {AssetId: "img_a"}},
	})
	if !ok {
		t.Fatal("the event did not translate")
	}
	if len(ev.Images) != 1 || ev.Images[0].AssetID != "img_a" {
		t.Errorf("Images = %+v, want just img_a", ev.Images)
	}
}
