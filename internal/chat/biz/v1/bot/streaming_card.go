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
	"encoding/json"
	"fmt"
	"log/slog"

	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/stream"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// ─── Streaming cards (cardkit: element_id + sequence incremental updates) ───

// Defaults for the cadence hints Feishu applies client-side when it animates a
// streaming card. They are what the card sends when a deployment configures
// nothing, and they are the values the wire format golden test pins.
const (
	defaultPrintFrequencyMs = 50
	defaultPrintStep        = 2
	defaultPrintStrategy    = "fast"
)

// cardkitPrint is Feishu's typing cadence for a streaming card: how often the
// client reveals buffered characters, how many at a time, and whether it favours
// staying current or rendering smoothly. It is a display hint only — the content
// the bot sends is unaffected.
type cardkitPrint struct {
	FrequencyMs int
	Step        int
	Strategy    string
}

// withDefaults fills in whatever the caller left unset, so a zero value means
// the platform defaults rather than a card that never animates.
func (p cardkitPrint) withDefaults() cardkitPrint {
	if p.FrequencyMs <= 0 {
		p.FrequencyMs = defaultPrintFrequencyMs
	}
	if p.Step <= 0 {
		p.Step = defaultPrintStep
	}
	if p.Strategy == "" {
		p.Strategy = defaultPrintStrategy
	}
	return p
}

const (
	// streamingCardElementID is the element_id of the card's main answer
	// markdown. It must match larkcard.RichCardMainTextElementID so the final
	// rich-card render targets the same element.
	streamingCardElementID = larkcard.RichCardMainTextElementID
	progressCardElementID  = "markdown_progress"
	reasoningCardElementID = "reasoning_1"
	stopCardElementID      = "stop_button"
	stopRowCardElementID   = "stop_row"
	// reasoningLivePanelTitle titles the reasoning panel while the turn runs. It
	// names what the panel holds and nothing else: a panel header cannot be updated
	// after the card is created, so a count or a duration here would be a claim the
	// card could never correct, and a state ("深度思考中…", thinking…) would go on claiming it
	// after the turn paused for a decision — which is exactly what it did, under a
	// card that was waiting for a human. It is the final card's own label for the
	// same content, so the panel does not rename itself when the turn ends.
	reasoningLivePanelTitle = larkcard.ReasoningPanelLabel
	// reasoningLiveInitialText is the reasoning panel's body before the first delta
	// arrives. The backend reports reasoning in chunks, so the panel is empty for
	// the opening moments of every turn — but only for turns whose card carries the
	// panel, which is why the panel is not created until the session asked for
	// reasoning at all.
	reasoningLiveInitialText = "思考中…"
	// toolLivePanelTitle titles the tool panel while the turn runs, and carries no
	// count for the same reason the reasoning panel carries none: the header is
	// frozen at creation while the list keeps growing, so a number here would be
	// wrong from the first tool onwards. The final card states the real count,
	// because it is built after the turn ends. It shares the label with that card's
	// panel so the two cannot be renamed apart.
	toolLivePanelTitle = larkcard.ToolPanelLabel
	// StopCardAction is the action value carried by the stop button.
	StopCardAction = "askai_stop"
	// stopButtonLabel is the button's whole visible text. A word rather than a
	// glyph: the stop icons are not in the card's token set, and an unrecognized
	// token is rejected when the card is parsed — taking the entire streaming card
	// down with it, which golden tests cannot catch. It is also the one button on
	// the card that is an action rather than a state, so it carries no state
	// character.
	stopButtonLabel = "停止"
)

// stopButton describes the card's stop button: the run it cancels and the
// conversation that run belongs to. A zero value means the card carries no
// button at all.
//
// The conversation has to travel in the callback value because the callback
// arrives without it. Lark reports who clicked and in which chat, but the
// session key is the conversation's own identity — for a group it is the chat
// id, for a direct message it is the *other* person's open id, which the
// platform does not report to the bot at all. Without it, a group click would
// be resolved against the clicker and land in a conversation that has no run.
type stopButton struct {
	RunID      string
	SessionKey string
	// ChatType is "p2p" or "group" when the receiver type is unambiguous, and
	// empty otherwise. It is left empty rather than guessed: the resolver trusts
	// this field, so a wrong guess is worse than no guess.
	ChatType string
}

// stopButtonFor builds the stop button for a receiver and the run it is running.
func stopButtonFor(recv *v1.Receiver, runID string) stopButton {
	stop := stopButton{RunID: runID}
	if recv == nil {
		return stop
	}
	stop.SessionKey = recv.Id
	switch {
	case recv.IsP2P():
		stop.ChatType = v1.ChatTypeP2P
	case recv.IsGroup():
		stop.ChatType = v1.ChatTypeGroup
	}
	return stop
}

// CreateStreamingCard creates a new streaming card, sends it to the receiver's
// conversation, and returns its card ID. The card entity must be delivered via a
// message after creation — cardkit's Card.Create only mints the entity, it does
// not surface it in any chat.
func (b *botBiz) CreateStreamingCard(ctx context.Context, recv *v1.Receiver, title, initialText string, showReasoning bool, stopRunID string) (string, error) {
	stop := stopButtonFor(recv, stopRunID)
	cardJSONBytes, err := buildStreamingCardJSON(title, initialText, showReasoning, stop, b.printHints())
	if err != nil {
		return "", err
	}

	req := larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().
			Type("card_json").
			Data(string(cardJSONBytes)).
			Build()).
		Build()

	resp, err := b.lark().Cardkit.V1.Card.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("cardkit create card: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("cardkit create card failed: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.CardId == nil {
		return "", fmt.Errorf("cardkit create card returned empty card_id")
	}
	cardID := *resp.Data.CardId

	// Pair this card with the run it can stop, so a click that arrives later can
	// be matched against the run that was actually in flight — including the
	// case where the click produced no log line at all, which is what an empty
	// stop button's value would look like from here.
	if stop.RunID != "" {
		slog.InfoContext(ctx, "streaming card created with a stop button",
			"card_id", cardID, "run_id", stop.RunID, "conv_id", stop.SessionKey, "chat_type", stop.ChatType)
	}

	// Deliver the card entity to the conversation. Card.Create only mints the
	// entity; without this send the streamed updates below would target an
	// invisible card and nothing would render in the chat.
	entityContent, err := cardEntityContent(cardID)
	if err != nil {
		return "", err
	}
	if _, err := b.sendMessage(ctx, recv, larkim.MsgTypeInteractive, entityContent); err != nil {
		return "", fmt.Errorf("send streaming card entity: %w", err)
	}

	return cardID, nil
}

// buildStreamingCardJSON builds the cardkit 2.0 streaming-card payload for one
// AI turn. It is split out of CreateStreamingCard so the wire format can be
// locked by golden tests without a live Lark client — the payload is what Feishu
// validates, and cardkit reports element_id/sequence mistakes only at runtime.
func buildStreamingCardJSON(title, initialText string, showReasoning bool, stop stopButton, print cardkitPrint) ([]byte, error) {
	print = print.withDefaults()

	// Body order is the order the turn unfolds: how it thought, what it ran, what it
	// answered, and last the button that stops it — which sits at the bottom however
	// long the answer above it grows.
	elements := make([]map[string]interface{}, 0, 4)
	if showReasoning {
		elements = append(elements, reasoningLivePanel(reasoningLiveInitialText))
	}
	elements = append(elements, toolLivePanel())
	elements = append(elements, map[string]interface{}{
		"tag": "markdown", "content": initialText, "element_id": streamingCardElementID,
	})
	if stop.RunID != "" {
		elements = append(elements, stopRowElement(stop))
	}
	config := larkcard.CardWidthConfig()
	config["streaming_mode"] = true
	// update_multi is what lets this card be changed component by component
	// after it exists (cardkit's CardElement.* endpoints refuse a card whose
	// update_multi is false), which is how the decision block is added and
	// removed while the card is on screen. It only governs collaborative
	// editing, so nothing else about the card changes.
	config["update_multi"] = true
	config["summary"] = map[string]interface{}{"content": "正在处理您的请求 ..."}
	config["enable_forward"] = true
	config["streaming_config"] = map[string]interface{}{
		"print_frequency_ms": map[string]interface{}{"default": print.FrequencyMs},
		"print_step":         map[string]interface{}{"default": print.Step},
		"print_strategy":     print.Strategy,
	}
	cardJSON := larkcard.NewCardRoot(config, elements)
	if title != "" {
		cardJSON["header"] = map[string]interface{}{
			"title":    map[string]interface{}{"content": title, "tag": "plain_text"},
			"template": "blue",
		}
	}

	return json.Marshal(cardJSON)
}

// reasoningLivePanel builds the live reasoning panel: the same house panel the
// final card draws, with a markdown body the streaming path addresses by id.
//
// Collapsed here as well as when the turn ends. An expanded panel of raw
// chain-of-thought would push the answer off the screen for the whole turn, which
// is exactly what putting the reasoning behind a panel avoids — and the panel
// keeps its state client-side, so a reader who opens it is not folded back up by
// the next content update.
func reasoningLivePanel(initial string) map[string]interface{} {
	panel := larkcard.PanelElement(reasoningLivePanelTitle, false, []map[string]interface{}{
		{"tag": "markdown", "content": initial, "element_id": reasoningCardElementID + "_text"},
	})
	panel["element_id"] = reasoningCardElementID
	return panel
}

// toolLivePanel wraps the live tool list in the same house panel as the final
// card's tool panel, expanded so a running step stays visible.
//
// The body starts empty: the panel exists from the moment the card is created, and
// an empty placeholder body is how "no tool has run yet" looks — it reads as
// nothing rather than as something broken. The first SetProgress fills it. (A
// component could also be added to the card later — cardkit's CardElement.Create
// does exactly that, which is how the decision block below arrives — but the panel
// is created up front because it must be in a fixed position, and inserting it
// later would move it down the card every time something else was appended.)
//
// Expanded here, collapsed in the final card, which is the one state difference
// between the two — the live list is the turn's progress report, the finished one
// is a record to consult. A step count in the title is deliberately not attempted;
// see toolLivePanelTitle.
func toolLivePanel() map[string]interface{} {
	return larkcard.PanelElement(toolLivePanelTitle, true, []map[string]interface{}{
		{"tag": "markdown", "content": "", "element_id": progressCardElementID},
	})
}

// stopRowElement builds the row holding the stop button, aligned to the card's
// right edge. The row is the card's last element, which is what puts it at the
// bottom: the streaming path re-renders the answer above it for the length of the
// turn, and an element that came before that answer would be pushed down the card
// rather than stay put.
//
// The row is right-aligned in two places on purpose. horizontal_align on the
// column_set places the row itself, and horizontal_align on the column keeps the
// button at the row's right edge even if the platform hands the auto-sized column
// more width than the button needs. One column only: it no longer shares the row
// with the progress line, so there is nothing to trade width with, and no
// flex_mode is needed to stop the button drifting with the tool text beside it.
func stopRowElement(stop stopButton) map[string]interface{} {
	value := map[string]interface{}{
		"action": StopCardAction,
		"run_id": stop.RunID,
	}
	if stop.SessionKey != "" {
		value["session_key"] = stop.SessionKey
	}
	if stop.ChatType != "" {
		value["chat_type"] = stop.ChatType
	}

	button := map[string]interface{}{
		"tag":        "button",
		"element_id": stopCardElementID,
		"text":       map[string]interface{}{"tag": "plain_text", "content": stopButtonLabel},
		"type":       "default",
		"size":       "small",
		"width":      "default",
		// The callback data is carried in both places Feishu accepts it: at the
		// top level (how every other button in this repo sends it, verified in
		// production) and in a callback behavior (the 2.0 form). Whichever the
		// platform honours, the click arrives identified; a value that only one
		// of them populates is indistinguishable from a dead button.
		"value": value,
		"behaviors": []map[string]interface{}{
			{"type": "callback", "value": value},
		},
	}

	return map[string]interface{}{
		"tag":              "column_set",
		"element_id":       stopRowCardElementID,
		"horizontal_align": "right",
		"columns": []map[string]interface{}{
			{
				"tag":              "column",
				"width":            "auto",
				"vertical_align":   "center",
				"horizontal_align": "right",
				"elements":         []map[string]interface{}{button},
			},
		},
	}
}

// cardEntityContent builds the interactive-message content that delivers a
// cardkit card entity into a chat. Feishu requires the {"type":"card","data":
// {"card_id":...}} wrapper here — a bare {"card_id":...} is parsed as card JSON
// and rejected with 200621 "parse card json err" (surfaced as 230099).
func cardEntityContent(cardID string) (string, error) {
	b, err := json.Marshal(map[string]interface{}{
		"type": "card",
		"data": map[string]string{"card_id": cardID},
	})
	if err != nil {
		return "", fmt.Errorf("marshal card entity content: %w", err)
	}
	return string(b), nil
}

// UpdateCardText updates the main answer element of a streaming card.
func (b *botBiz) UpdateCardText(ctx context.Context, cardID, content string, seq *stream.Sequence) error {
	return b.updateCardElement(ctx, cardID, streamingCardElementID, content, seq.Next())
}

// UpdateCardReasoningText updates the reasoning element of a streaming card.
func (b *botBiz) UpdateCardReasoningText(ctx context.Context, cardID, content string, seq *stream.Sequence) error {
	return b.updateCardElement(ctx, cardID, reasoningCardElementID+"_text", content, seq.Next())
}

// UpdateCardProgressText updates the progress element of a streaming card.
func (b *botBiz) UpdateCardProgressText(ctx context.Context, cardID, content string, seq *stream.Sequence) error {
	return b.updateCardElement(ctx, cardID, progressCardElementID, content, seq.Next())
}

func (b *botBiz) updateCardElement(ctx context.Context, cardID, elementID, content string, sequence int64) error {
	req := larkcardkit.NewContentCardElementReqBuilder().
		CardId(cardID).
		ElementId(elementID).
		Body(larkcardkit.NewContentCardElementReqBodyBuilder().
			Content(content).
			Sequence(int(sequence)).
			Build()).
		Build()

	resp, err := b.lark().Cardkit.V1.CardElement.Content(ctx, req)
	if err != nil {
		return fmt.Errorf("cardkit update card element: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("cardkit update card element failed: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// UpdateCardElementText writes new text into one of the card's markdown elements,
// addressed by id. Unlike UpdateCardText it is not tied to the answer element, so
// a caller that owns another markdown component — the decision log — can rewrite
// it without the turn's preview knowing.
func (b *botBiz) UpdateCardElementText(ctx context.Context, cardID, elementID, content string, seq *stream.Sequence) error {
	return b.updateCardElement(ctx, cardID, elementID, content, seq.Next())
}

// InsertCardElements adds components to a card that is already on screen, placing
// them immediately before the element named by target. An empty target appends
// them to the end of the card body.
//
// This is what makes the decision block possible: the card is created before
// anything is known about whether the run will pause, and a pause has to put its
// buttons on the card the reader is already looking at rather than on a second
// message. Every element write shares the turn's sequence with the streaming
// text, so the platform orders them as the turn produced them.
func (b *botBiz) InsertCardElements(ctx context.Context, cardID, target string, seq *stream.Sequence, elements ...map[string]any) error {
	if len(elements) == 0 {
		return nil
	}
	encoded, err := json.Marshal(elements)
	if err != nil {
		return fmt.Errorf("marshal card elements: %w", err)
	}

	insertType := larkcardkit.TypeAppend
	if target != "" {
		insertType = larkcardkit.TypeInsertBefore
	}
	body := larkcardkit.NewCreateCardElementReqBodyBuilder().
		Type(insertType).
		Elements(string(encoded)).
		Sequence(int(seq.Next()))
	if target != "" {
		body = body.TargetElementId(target)
	}
	req := larkcardkit.NewCreateCardElementReqBuilder().
		CardId(cardID).
		Body(body.Build()).
		Build()

	resp, err := b.lark().Cardkit.V1.CardElement.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("cardkit create card element: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("cardkit create card element failed: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// DeleteCardElements removes components from a card by id. A missing one is the
// platform's error to report: the caller deletes what it put there, and a
// component it cannot name is a bug on this side rather than a state to tolerate.
func (b *botBiz) DeleteCardElements(ctx context.Context, cardID string, seq *stream.Sequence, ids ...string) error {
	for _, id := range ids {
		req := larkcardkit.NewDeleteCardElementReqBuilder().
			CardId(cardID).
			ElementId(id).
			Body(larkcardkit.NewDeleteCardElementReqBodyBuilder().
				Sequence(int(seq.Next())).
				Build()).
			Build()

		resp, err := b.lark().Cardkit.V1.CardElement.Delete(ctx, req)
		if err != nil {
			return fmt.Errorf("cardkit delete card element %s: %w", id, err)
		}
		if !resp.Success() {
			return fmt.Errorf("cardkit delete card element %s failed: code=%d, msg=%s", id, resp.Code, resp.Msg)
		}
	}
	return nil
}

// SetCardSummary replaces the summary the chat list shows for a card. It is the
// one config field worth writing mid-turn: the summary is what a reader sees in
// the conversation list, and a card parked on a decision should say so there
// rather than keep claiming the request is being processed.
func (b *botBiz) SetCardSummary(ctx context.Context, cardID, summary string, seq *stream.Sequence) error {
	settings, err := json.Marshal(map[string]any{
		"config": map[string]any{"summary": map[string]any{"content": summary}},
	})
	if err != nil {
		return fmt.Errorf("marshal card settings: %w", err)
	}

	req := larkcardkit.NewSettingsCardReqBuilder().
		CardId(cardID).
		Body(larkcardkit.NewSettingsCardReqBodyBuilder().
			Settings(string(settings)).
			Sequence(int(seq.Next())).
			Build()).
		Build()

	resp, err := b.lark().Cardkit.V1.Card.Settings(ctx, req)
	if err != nil {
		return fmt.Errorf("cardkit update card settings: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("cardkit update card settings failed: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// CloseStreamingCard finalizes a streaming card by replacing it with a complete
// final card (prebuilt via larkcard.BuildRichCard). The full-card update is the
// only cardkit API that can change the header, since the Settings endpoint
// accepts only the config block.
func (b *botBiz) CloseStreamingCard(ctx context.Context, cardID, cardJSON string, seq *stream.Sequence) error {
	card := larkcardkit.NewCardBuilder().Type("card_json").Data(cardJSON).Build()
	req := larkcardkit.NewUpdateCardReqBuilder().
		CardId(cardID).
		Body(larkcardkit.NewUpdateCardReqBodyBuilder().
			Card(card).
			Sequence(int(seq.Next())).
			Build()).
		Build()

	resp, err := b.lark().Cardkit.V1.Card.Update(ctx, req)
	if err != nil {
		return fmt.Errorf("cardkit finalize streaming card: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("cardkit finalize streaming card failed: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	return nil
}
