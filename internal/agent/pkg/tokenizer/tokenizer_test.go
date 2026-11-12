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

package tokenizer

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	tiktokenloader "github.com/pkoukk/tiktoken-go-loader"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

func TestCount_ChineseAndEnglish(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "empty", text: ""},
		{name: "ascii", text: "hello world, this is a plain english sentence."},
		{name: "chinese", text: "这是一段中文文本，用来验证中文分词计数是否正确。"},
		{name: "mixed", text: "请帮我 review 这个 Kubernetes deployment 的 yaml 配置"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Count(tt.text)
			if err != nil {
				t.Fatalf("Count(%q) returned error: %v", tt.text, err)
			}
			if tt.text == "" && got != 0 {
				t.Fatalf("Count(%q) = %d, want 0 for empty input", tt.text, got)
			}
			if tt.text != "" && got <= 0 {
				t.Fatalf("Count(%q) = %d, want a positive count", tt.text, got)
			}
		})
	}
}

// TestOfflineLoaderResolvesEncodingsURL is the offline guarantee.
//
// tiktoken-go asks its BPE loader for exactly this URL when it initialises an
// encoding. If the offline loader can serve that URL from its embedded tables,
// no network fetch is possible. If this test ever fails, the process would be
// downloading tokenizer tables at runtime.
func TestOfflineLoaderResolvesEncodingsURL(t *testing.T) {
	const url = "https://openaipublic.blob.core.windows.net/encodings/cl100k_base.tiktoken"

	ranks, err := tiktokenloader.NewOfflineLoader().LoadTiktokenBpe(url)
	if err != nil {
		t.Fatalf("offline loader could not serve %s: %v", url, err)
	}
	if len(ranks) == 0 {
		t.Fatalf("offline loader returned an empty table for %s", url)
	}
}

// TestCount_GrowsWithLength is a sanity check that the count is not degenerate
// (for example, returning a constant), which would silently disable every
// budget guard built on top of it.
func TestCount_GrowsWithLength(t *testing.T) {
	small, err := Count(strings.Repeat("运维排查 ", 10))
	if err != nil {
		t.Fatalf("Count returned error: %v", err)
	}
	large, err := Count(strings.Repeat("运维排查 ", 1000))
	if err != nil {
		t.Fatalf("Count returned error: %v", err)
	}
	if large <= small {
		t.Fatalf("count did not grow with input length: small=%d large=%d", small, large)
	}
}

func TestCountMessages_ChargesImagesFlat(t *testing.T) {
	// A data URL is mostly base64 payload. Counting it as text would dwarf the
	// real cost and compact on every image-carrying turn, so images get a flat
	// allowance instead.
	const dataURL = "data:image/png;base64," + "iVBORw0KGgoAAAANSUhEUg" // stand-in payload

	withImage, err := CountMessages([]agentapi.Message{{Role: agentapi.RoleUser, Images: []media.Ref{{URL: dataURL}}}})
	if err != nil {
		t.Fatalf("CountMessages returned error: %v", err)
	}
	if withImage != tokensPerImage {
		t.Fatalf("CountMessages with one image = %d, want %d", withImage, tokensPerImage)
	}

	twoImages, err := CountMessages([]agentapi.Message{{Role: agentapi.RoleUser, Images: []media.Ref{{URL: dataURL}, {URL: dataURL}}}})
	if err != nil {
		t.Fatalf("CountMessages returned error: %v", err)
	}
	if twoImages != 2*tokensPerImage {
		t.Fatalf("CountMessages with two images = %d, want %d", twoImages, 2*tokensPerImage)
	}
}

func TestCountSchemaMessages_MultimodalContentNotDoubleCounted(t *testing.T) {
	// toSchemaMessages copies Content into a text part when images are present,
	// so a message carrying both must be charged for Content exactly once.
	const content = "看看这张图"

	textOnly, err := CountSchemaMessages([]*schema.Message{schema.UserMessage(content)}, nil)
	if err != nil {
		t.Fatalf("CountSchemaMessages returned error: %v", err)
	}

	url := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg"
	multimodal := schema.UserMessage(content)
	multimodal.UserInputMultiContent = []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeText, Text: content},
		{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
			MessagePartCommon: schema.MessagePartCommon{URL: &url},
		}},
	}
	withImage, err := CountSchemaMessages([]*schema.Message{multimodal}, nil)
	if err != nil {
		t.Fatalf("CountSchemaMessages returned error: %v", err)
	}

	want := textOnly + tokensPerImage
	if withImage != want {
		t.Fatalf("multimodal count = %d, want %d (text counted once plus one image)", withImage, want)
	}
}

func TestCountSchemaMessages_CountsToolSchemas(t *testing.T) {
	// adk re-sends tool schemas on every model call, so they must consume the
	// budget even when the message list is empty.
	without, err := CountSchemaMessages(nil, nil)
	if err != nil {
		t.Fatalf("CountSchemaMessages returned error: %v", err)
	}

	tool := &schema.ToolInfo{
		Name: "k8s_get_pod",
		Desc: "Fetch a pod by namespace and name from the target cluster.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"namespace": {Type: schema.String, Desc: "the namespace to look in", Required: true},
			"name":      {Type: schema.String, Desc: "the pod name", Required: true},
		}),
	}
	with, err := CountSchemaMessages(nil, []*schema.ToolInfo{tool})
	if err != nil {
		t.Fatalf("CountSchemaMessages returned error: %v", err)
	}
	if with <= without {
		t.Fatalf("tool schema was not counted: without=%d with=%d", without, with)
	}
}

// BenchmarkCountSchemaMessages measures the cost the budget guard pays on every
// model call, since adk invokes the summarization TokenCounter each iteration.
func BenchmarkCountSchemaMessages(b *testing.B) {
	msgs := make([]*schema.Message, 0, 32)
	for range 16 {
		msgs = append(msgs,
			schema.UserMessage(strings.Repeat("请帮我排查这个线上问题，涉及 Kubernetes Pod 状态异常。", 40)),
			schema.AssistantMessage(strings.Repeat("I checked the pod and it is in CrashLoopBackOff. ", 40), nil),
		)
	}
	for b.Loop() {
		if _, err := CountSchemaMessages(msgs, nil); err != nil {
			b.Fatal(err)
		}
	}
}
