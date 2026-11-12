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
// The original repo for this file is https://github.com/onexstack/onex.

// Package rag answers a question from one document the caller names.
//
// It is the short path for "read this and tell me": the document does not fit
// in a prompt, so it is chunked, the chunks are ranked against the question,
// and only the best few are handed to the model to answer from. That is also
// what keeps the answer citable — the model sees an excerpt list rather than a
// wall of text, and is asked to reference it.
//
// The stages are plain Go rather than a compose workflow. The workflow version
// this was ported from wired four nodes together to express a sequence, a
// fan-out and a fan-in — all of which a bounded worker pool says in fewer lines
// and with a stack trace that points at the failing stage rather than at a
// graph address.
package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/pkg/modeljson"
)

// ToolName is the name the model sees.
const ToolName = "answer_from_document"

const (
	// chunkSize is the target size of one chunk in characters. It is small
	// enough that a handful of chunks fit in a prompt and large enough that a
	// chunk still carries a coherent argument.
	chunkSize = 4000
	// scoreConcurrency bounds how many chunks are ranked at once. Ranking is
	// one model call per chunk, so this is the knob that decides how hard a
	// single question hits the model endpoint.
	scoreConcurrency = 5
	// topK is how many chunks reach the synthesis step.
	topK = 5
	// maxDocumentBytes bounds what will be read off disk. A tool that reads a
	// path the model chose must not be able to pull an arbitrarily large file
	// into memory.
	maxDocumentBytes = 8 << 20
)

// Config configures the tool.
type Config struct {
	// Model ranks chunks and writes the answer. Required.
	Model model.BaseChatModel
	// ChunkSize, ScoreConcurrency, TopK and MaxDocumentBytes override the
	// defaults above. Zero means the default.
	ChunkSize        int
	ScoreConcurrency int
	TopK             int
	MaxDocumentBytes int
}

// input is the tool's argument shape.
type input struct {
	FilePath string `json:"file_path"`
	Question string `json:"question"`
}

// answerFromDocumentTool implements the tool.
type answerFromDocumentTool struct {
	cfg Config
}

// NewTool builds the tool, or nil when no model is configured.
func NewTool(cfg Config) tool.BaseTool {
	if cfg.Model == nil {
		return nil
	}
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = chunkSize
	}
	if cfg.ScoreConcurrency <= 0 {
		cfg.ScoreConcurrency = scoreConcurrency
	}
	if cfg.TopK <= 0 {
		cfg.TopK = topK
	}
	if cfg.MaxDocumentBytes <= 0 {
		cfg.MaxDocumentBytes = maxDocumentBytes
	}
	return &answerFromDocumentTool{cfg: cfg}
}

// Info advertises the tool.
func (t *answerFromDocumentTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: ToolName,
		Desc: "从一份文档中回答问题。适用于用户指定了某个文件、并就该文件提问的场景；" +
			"文档较长时本工具会先筛选出相关段落再作答，答案会引用所依据的段落编号。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_path": {
				Type:     schema.String,
				Desc:     "文档的绝对路径。",
				Required: true,
			},
			"question": {
				Type:     schema.String,
				Desc:     "要回答的问题。",
				Required: true,
			},
		}),
	}, nil
}

// InvokableRun runs the three stages.
func (t *answerFromDocumentTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var in input
	if err := json.Unmarshal([]byte(argumentsInJSON), &in); err != nil {
		return "", fmt.Errorf("expected an object with file_path and question: %w", err)
	}
	if strings.TrimSpace(in.FilePath) == "" {
		return "", fmt.Errorf("file_path cannot be empty")
	}
	if strings.TrimSpace(in.Question) == "" {
		return "", fmt.Errorf("question cannot be empty")
	}

	text, err := readDocument(in.FilePath, t.cfg.MaxDocumentBytes)
	if err != nil {
		return "", err
	}

	chunks := splitIntoChunks(text, t.cfg.ChunkSize)
	if len(chunks) == 0 {
		return "The document is empty.", nil
	}

	scored := t.scoreAll(ctx, chunks, in.Question)
	kept := bestN(scored, t.cfg.TopK)
	if len(kept) == 0 {
		// Ranking can legitimately return nothing when every chunk scores zero.
		// Falling back to the first chunks is better than refusing to answer:
		// the question is about this document, so its beginning is at least on
		// topic, and "no answer" would be indistinguishable from "unreadable".
		slog.DebugContext(ctx, "rag: no chunk scored above zero; answering from the document's start",
			"file", in.FilePath)
		kept = chunks[:min(t.cfg.TopK, len(chunks))]
	}

	answer, err := t.synthesize(ctx, in.Question, kept)
	if err != nil {
		return "", err
	}
	return answer, nil
}

// readDocument reads the file, refusing anything larger than the cap.
func readDocument(path string, maxBytes int) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a document", path)
	}
	if info.Size() > int64(maxBytes) {
		return "", fmt.Errorf("%s is %d bytes, larger than the %d byte limit", path, info.Size(), maxBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", path, err)
	}
	return string(raw), nil
}

// chunk is one scored passage.
type chunk struct {
	text    string
	score   int
	excerpt string
}

// scoreAll ranks every chunk against the question, a bounded number at a time.
//
// The bound is on workers, not just on concurrent calls: spawning a goroutine
// per chunk means a large document starts thousands of them and lets the
// scheduler hold them, for no gain over a pool of the size that will actually
// run.
//
// A chunk whose ranking fails scores zero rather than aborting the call: one
// unusable passage is not a reason to refuse the question. Ranking order is
// recovered by the sort in bestN, so running them concurrently costs no
// determinism.
func (t *answerFromDocumentTool) scoreAll(ctx context.Context, chunks []string, question string) []chunk {
	scored := make([]chunk, len(chunks))
	workers := min(t.cfg.ScoreConcurrency, len(chunks))

	indexes := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range indexes {
				scored[i] = t.scoreOne(ctx, chunks[i], question)
			}
		})
	}
	for i := range chunks {
		indexes <- i
	}
	close(indexes)
	wg.Wait()
	return scored
}

// scoreOne asks the model to rate one chunk and quote its most relevant line.
func (t *answerFromDocumentTool) scoreOne(ctx context.Context, text, question string) chunk {
	prompt := fmt.Sprintf(`判断下面这段文本与问题的相关程度。

问题：%s

文本：
%s

严格返回 JSON，不要有任何其他内容：
{"score": <0-10>, "excerpt": "<最能回答问题的句子或短语，得分为 0 时留空>"}

评分参考：0=完全无关，3=仅有一点关联，7=明显相关，10=直接回答了问题。`,
		question, text)

	resp, err := t.cfg.Model.Generate(ctx, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil || resp == nil {
		slog.DebugContext(ctx, "rag: chunk ranking failed; treating the chunk as irrelevant", "error", err)
		return chunk{text: text}
	}

	var parsed struct {
		Score   int    `json:"score"`
		Excerpt string `json:"excerpt"`
	}
	if err := json.Unmarshal([]byte(modeljson.StripFence(resp.Content)), &parsed); err != nil {
		slog.DebugContext(ctx, "rag: unparsable chunk ranking; treating the chunk as irrelevant", "error", err)
		return chunk{text: text}
	}
	return chunk{text: text, score: parsed.Score, excerpt: parsed.Excerpt}
}

// bestN returns the n highest-scoring chunks, best first.
func bestN(scored []chunk, n int) []string {
	kept := make([]chunk, 0, len(scored))
	for _, c := range scored {
		if c.score > 0 {
			kept = append(kept, c)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].score > kept[j].score })
	if len(kept) > n {
		kept = kept[:n]
	}

	out := make([]string, 0, len(kept))
	for _, c := range kept {
		if c.excerpt != "" {
			out = append(out, c.excerpt)
			continue
		}
		out = append(out, c.text)
	}
	return out
}

// synthesize writes the answer from the selected excerpts.
func (t *answerFromDocumentTool) synthesize(ctx context.Context, question string, excerpts []string) (string, error) {
	var sb strings.Builder
	sb.WriteString("只依据下面提供的文档片段回答问题。\n\n问题：")
	sb.WriteString(question)
	sb.WriteString("\n\n文档片段：\n")
	for i, excerpt := range excerpts {
		fmt.Fprintf(&sb, "[%d] %s\n\n", i+1, excerpt)
	}
	sb.WriteString("请给出清晰简洁的回答。引用片段时请标注编号，如 [1]。")

	resp, err := t.cfg.Model.Generate(ctx, []*schema.Message{schema.UserMessage(sb.String())})
	if err != nil {
		return "", fmt.Errorf("rag: synthesize: %w", err)
	}
	if resp == nil {
		return "", fmt.Errorf("rag: synthesize returned no message")
	}
	return resp.Content, nil
}

// splitIntoChunks splits text into chunks of at most chunkSize characters,
// preferring paragraph boundaries and falling back to line boundaries for a
// paragraph that is itself too long.
func splitIntoChunks(text string, chunkSize int) []string {
	var (
		chunks []string
		buf    strings.Builder
	)
	flush := func() {
		if s := strings.TrimSpace(buf.String()); s != "" {
			chunks = append(chunks, s)
		}
		buf.Reset()
	}

	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if buf.Len() > 0 && buf.Len()+len(para)+2 > chunkSize {
			flush()
		}
		if len(para) <= chunkSize {
			if buf.Len() > 0 {
				buf.WriteString("\n\n")
			}
			buf.WriteString(para)
			continue
		}
		// A single paragraph over the limit: split it by line, and a single
		// line over the limit by character, so no chunk can exceed the target.
		for _, line := range strings.Split(para, "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			if buf.Len() > 0 && buf.Len()+len(line)+1 > chunkSize {
				flush()
			}
			for len(line) > chunkSize {
				if buf.Len() > 0 {
					flush()
				}
				chunks = append(chunks, line[:chunkSize])
				line = line[chunkSize:]
			}
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(line)
		}
	}
	flush()
	return chunks
}
