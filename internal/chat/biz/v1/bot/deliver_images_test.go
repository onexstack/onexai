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

package bot

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"

	agentfake "github.com/onexstack/onexai/pkg/clientset/agent/fake"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/pkg/media"
)

// deliveryTurn builds just enough of a turn for deliverImages: the bot, the
// runtime, and nothing else. The timers and the streaming preview a full turn
// starts are not part of what is under test, which is the path a drawn picture
// takes from the store to the conversation.
func deliveryTurn(lark *fakeLarkServices, content map[string][]byte) *aiTurn {
	return &aiTurn{
		b: &botBiz{sr: &runtime.StaticRuntime{
			Clientset: &fakeClientset{llm: &agentfake.Client{AssetContent: content}},
		}},
		dr: &runtime.DynamicRuntime{
			Receiver:     &v1.Receiver{Id: "oc_1", Type: v1.ReceiverTypeChatID},
			LarkServices: lark,
		},
	}
}

// TestDeliverImagesSendsEachDrawnImage covers the last hop of the image
// generation chain, which until this existed had no test at all.
//
// The bytes come from the store the agent backend owns — this service holds
// none — and go out through the Lark port one message per picture, because a
// rich card cannot carry a conversation image.
func TestDeliverImagesSendsEachDrawnImage(t *testing.T) {
	lark := &fakeLarkServices{}
	turn := deliveryTurn(lark, map[string][]byte{
		"img_a": []byte("PNG-A"),
		"img_b": []byte("PNG-B"),
	})

	turn.deliverImages(context.Background(), []media.Ref{{AssetID: "img_a"}, {AssetID: "img_b"}})

	sent := lark.sentImages()
	if len(sent) != 2 {
		t.Fatalf("sent %d images, want 2", len(sent))
	}
	if string(sent[0]) != "PNG-A" || string(sent[1]) != "PNG-B" {
		t.Errorf("sent %q and %q, want the bytes the store returned", sent[0], sent[1])
	}
}

// TestDeliverImagesKeepsGoingPastAFailure pins what a batch does when one of
// its pictures cannot be had.
//
// A picture that cannot be read and one that cannot be sent are the same answer
// here — that one did not arrive — and neither is a reason to withhold the
// rest. Stopping at the first failure would turn three drawings into one
// delivered picture and two silent losses, which looks from the outside like a
// model that drew less than it did.
func TestDeliverImagesKeepsGoingPastAFailure(t *testing.T) {
	t.Run("an unreadable picture does not stop the batch", func(t *testing.T) {
		lark := &fakeLarkServices{}
		turn := deliveryTurn(lark, map[string][]byte{"img_b": []byte("PNG-B")})

		// img_missing is not in the store; img_b is.
		turn.deliverImages(context.Background(), []media.Ref{
			{AssetID: "img_missing"}, {AssetID: "img_b"},
		})

		sent := lark.sentImages()
		if len(sent) != 1 || string(sent[0]) != "PNG-B" {
			t.Errorf("sent %q, want just the picture that could be read", sent)
		}
	})

	t.Run("a refused send does not stop the batch", func(t *testing.T) {
		lark := &fakeLarkServices{imageErr: errors.New("lark refused the send")}
		turn := deliveryTurn(lark, map[string][]byte{"img_a": []byte("PNG-A")})

		// Nothing is delivered and nothing panics: a failed send is logged by
		// the turn, and the answer it came with has already been written.
		turn.deliverImages(context.Background(), []media.Ref{{AssetID: "img_a"}})
		if len(lark.sentImages()) != 0 {
			t.Errorf("a refused send was recorded as delivered")
		}
	})
}

// TestDeliverImagesWithoutALarkSurface pins the console-only case.
//
// A turn with no Lark services has nowhere to send a picture — the mirror is
// the only place it goes — and reaching the concrete sender anyway would fail
// per image for a reason that is not a failure.
func TestDeliverImagesWithoutALarkSurface(t *testing.T) {
	turn := deliveryTurn(nil, map[string][]byte{"img_a": []byte("PNG-A")})
	turn.dr.LarkServices = nil

	// No panic, and nothing attempted: there is no sender to attempt it with.
	turn.deliverImages(context.Background(), []media.Ref{{AssetID: "img_a"}})
}

// TestDeliverImagesWithNothingToSend pins the empty batch, which is the common
// case: most turns draw nothing.
func TestDeliverImagesWithNothingToSend(t *testing.T) {
	lark := &fakeLarkServices{}
	turn := deliveryTurn(lark, nil)

	turn.deliverImages(context.Background(), nil)
	if len(lark.sentImages()) != 0 {
		t.Errorf("sent %q for an empty batch", lark.sentImages())
	}
}
