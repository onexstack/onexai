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

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/stream"
)

// larkStreamSink renders a stream.Preview onto one cardkit streaming card.
//
// It is the only place in this package that knows which card element carries
// which lane, so the mapping between a lane and an element id exists once. The
// sink holds no state of its own: a Preview calls it one lane at a time, in
// order, and owns the sequence they are numbered from.
type larkStreamSink struct {
	svc    runtime.LarkServices
	cardID string
}

func (s *larkStreamSink) UpdateText(ctx context.Context, seq *stream.Sequence, text string) error {
	return s.svc.UpdateCardText(ctx, s.cardID, text, seq)
}

func (s *larkStreamSink) UpdateReasoning(ctx context.Context, seq *stream.Sequence, text string) error {
	return s.svc.UpdateCardReasoningText(ctx, s.cardID, text, seq)
}

func (s *larkStreamSink) UpdateProgress(ctx context.Context, seq *stream.Sequence, text string) error {
	return s.svc.UpdateCardProgressText(ctx, s.cardID, text, seq)
}

// streamSink returns the renderer for one turn's card, or nil when incremental
// publishing is off. A nil sink is not a degradation: the preview still
// accumulates the answer and the card receives it in one piece when the turn
// closes.
func (b *botBiz) streamSink(dr *runtime.DynamicRuntime, cardID string) stream.Sink {
	if !b.sr.StreamingEnabled || dr.LarkServices == nil {
		return nil
	}
	return &larkStreamSink{svc: dr.LarkServices, cardID: cardID}
}

// streamConfig returns the publish throttle configured for this deployment.
func (b *botBiz) streamConfig() stream.Config {
	return b.sr.Streaming
}

// printHints returns the typing cadence Feishu should animate this deployment's
// streaming cards with. cardkitPrint fills in the platform defaults for anything
// the configuration leaves unset.
func (b *botBiz) printHints() cardkitPrint {
	return cardkitPrint{
		FrequencyMs: b.sr.StreamingPrintFrequencyMs,
		Step:        b.sr.StreamingPrintStep,
		Strategy:    b.sr.StreamingPrintStrategy,
	}
}
