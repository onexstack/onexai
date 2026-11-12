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

package skill

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk/filesystem"
	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/schema"
	larkv3 "github.com/larksuite/oapi-sdk-go/v3"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	larkwiki "github.com/larksuite/oapi-sdk-go/v3/service/wiki/v2"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexstack/pkg/astore"

	"github.com/onexstack/onexai/internal/agent/model"
)

func init() {
	Register(ProviderSpec{
		Name:    ProviderLarkDoc,
		Aliases: []string{"feishu_doc"},
		Build: func(_ context.Context, s *model.SkillM, spec BuildSpec) (FullBackend, error) {
			var params model.LarkDocParams
			if err := decodeParams(s.Params, &params); err != nil {
				return nil, err
			}
			if params.DocToken == "" {
				return nil, fmt.Errorf("lark_doc: doc_token is required")
			}
			if spec.Deps.LarkClient == nil {
				return nil, fmt.Errorf("lark_doc: no Lark client is configured")
			}
			return NewLarkDocBackend(spec.Deps.LarkClient, spec.SkillsDir, spec.AgentKey, LarkDocConfig{
				DocToken: params.DocToken,
				CacheTTL: spec.CacheTTL,
			})
		},
	})
}

// larkDocMaxDepth bounds how far a document's references are followed. Depth 0
// is the configured document; its referenced documents are downloaded as
// reference files but their own references are left as links, which is what
// stops a cycle of documents referencing each other from downloading forever.
const larkDocMaxDepth = 0

// LarkDocConfig configures a Feishu-document skill source.
type LarkDocConfig struct {
	// DocToken identifies the document: a docx token, a wiki node token, or a
	// full Feishu URL.
	DocToken string
	// CacheTTL is how long the converted document is served before re-reading.
	CacheTTL time.Duration
}

// larkDocBackend loads skills from a Feishu document.
//
// Two shapes are supported, and which one applies is decided by the document
// itself rather than by configuration: a document that declares itself a skill
// is that one skill, and a document that is a plain index contributes every
// skill it references. That is what lets an operator keep a wiki page as the
// registry of an agent set's skills.
type larkDocBackend struct {
	larkClient *larkv3.Client
	skillsDir  string
	agentKey   string
	cfg        LarkDocConfig
	local      *localbk.Local
	cache      *astore.Store[map[string]adkskill.Skill]
}

// NewLarkDocBackend builds a backend for one Feishu document.
func NewLarkDocBackend(larkClient *larkv3.Client, skillsDir, agentKey string, cfg LarkDocConfig) (FullBackend, error) {
	if larkClient == nil {
		return nil, fmt.Errorf("lark_doc: client is required")
	}
	if cfg.DocToken == "" {
		return nil, fmt.Errorf("lark_doc: doc_token is required")
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = defaultCacheTTL
	}
	localFS, err := localbk.NewBackend(context.Background(), &localbk.Config{})
	if err != nil {
		return nil, fmt.Errorf("lark_doc: local backend: %w", err)
	}
	b := &larkDocBackend{
		larkClient: larkClient,
		skillsDir:  skillsDir,
		agentKey:   agentKey,
		cfg:        cfg,
		local:      localFS,
	}
	b.cache = astore.New(astore.Options{Name: "skill.lark_doc", TTL: cfg.CacheTTL}, b.load)
	return b, nil
}

// cachedSkills returns the converted document, reloading it when stale.
func (b *larkDocBackend) cachedSkills(ctx context.Context) (map[string]adkskill.Skill, error) {
	gen, err := b.cache.LoadE(ctx)
	if err != nil {
		return nil, err
	}
	return gen.Value, nil
}

// List returns the document's skills, sorted by name so the order is stable
// across refreshes.
func (b *larkDocBackend) List(ctx context.Context) ([]adkskill.FrontMatter, error) {
	skills, err := b.cachedSkills(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]adkskill.FrontMatter, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.FrontMatter)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one skill by name.
func (b *larkDocBackend) Get(ctx context.Context, name string) (adkskill.Skill, error) {
	skills, err := b.cachedSkills(ctx)
	if err != nil {
		return adkskill.Skill{}, err
	}
	skill, ok := skills[name]
	if !ok {
		return adkskill.Skill{}, fmt.Errorf("skill not found: %s", name)
	}
	return skill, nil
}

// load converts the configured document and every skill it contains.
//
// blockCache is scoped to one load so a document referenced by several pages is
// fetched from Feishu once, not once per reference.
func (b *larkDocBackend) load(ctx context.Context) (map[string]adkskill.Skill, error) {
	blockCache := make(map[string][]*larkdocx.Block)

	mainContent, referenced, err := b.convertDocToMarkdown(ctx, b.cfg.DocToken, larkDocMaxDepth, blockCache)
	if err != nil {
		return nil, fmt.Errorf("lark_doc: fetch primary document: %w", err)
	}

	skills := make(map[string]adkskill.Skill)
	if isSkill, fm := isSkillDocument(mainContent); isSkill {
		skill, err := b.buildSkill(ctx, fm, mainContent, referenced, blockCache)
		if err != nil {
			return nil, fmt.Errorf("lark_doc: build skill from primary document: %w", err)
		}
		skills[skill.FrontMatter.Name] = skill
		return skills, nil
	}

	// The document is an index: every referenced document that declares itself
	// a skill contributes one.
	for refToken := range referenced {
		content, subRefs, err := b.convertDocToMarkdown(ctx, refToken, larkDocMaxDepth, blockCache)
		if err != nil {
			slog.WarnContext(ctx, "[skill/lark_doc] referenced document unreadable, skipping", "token", refToken, "error", err)
			continue
		}
		isSkill, fm := isSkillDocument(content)
		if !isSkill {
			continue
		}
		skill, err := b.buildSkill(ctx, fm, content, subRefs, blockCache)
		if err != nil {
			slog.WarnContext(ctx, "[skill/lark_doc] referenced skill unusable, skipping", "token", refToken, "skill", fm.Name, "error", err)
			continue
		}
		skills[skill.FrontMatter.Name] = skill
	}
	return skills, nil
}

// buildSkill materialises one skill: SKILL.md plus its first-level references.
func (b *larkDocBackend) buildSkill(
	ctx context.Context, fm adkskill.FrontMatter, content string,
	referenced map[string]bool, blockCache map[string][]*larkdocx.Block,
) (adkskill.Skill, error) {
	skill := adkskill.Skill{FrontMatter: fm, Content: content}
	if b.skillsDir == "" {
		return skill, nil
	}

	baseDir := filepath.Join(b.skillsDir, AgentDirSegment(b.agentKey), ProviderLarkDoc, fm.Name)
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return adkskill.Skill{}, fmt.Errorf("create skill directory: %w", err)
	}
	if err := writeFileIfChanged(filepath.Join(baseDir, skillFileName), []byte(content)); err != nil {
		return adkskill.Skill{}, fmt.Errorf("write %s: %w", skillFileName, err)
	}

	if err := b.writeReferences(ctx, baseDir, referenced, blockCache); err != nil {
		return adkskill.Skill{}, err
	}

	skill.BaseDirectory = baseDir
	return skill, nil
}

// writeReferences downloads a skill's referenced documents into references/.
//
// A referenced document that is itself a skill is skipped: it is already its
// own skill, and copying it in as a reference would nest one skill inside
// another. The filename drops the "wiki:" prefix to match the link the renderer
// wrote into SKILL.md — the prefix stays on the token because resolving it
// needs to know it is a wiki node, but a filename must not contain a colon.
func (b *larkDocBackend) writeReferences(
	ctx context.Context, baseDir string, referenced map[string]bool, blockCache map[string][]*larkdocx.Block,
) error {
	if len(referenced) == 0 {
		return nil
	}
	refDir := filepath.Join(baseDir, "references")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		return fmt.Errorf("create references directory: %w", err)
	}
	for token := range referenced {
		// Depth 1 keeps the reference's own links as links rather than
		// following them, which is what bounds the traversal.
		content, _, err := b.convertDocToMarkdown(ctx, token, larkDocMaxDepth+1, blockCache)
		if err != nil {
			slog.WarnContext(ctx, "[skill/lark_doc] referenced document unreadable, skipping", "token", token, "error", err)
			continue
		}
		if isSkill, _ := isSkillDocument(content); isSkill {
			continue
		}
		name := fmt.Sprintf("referenced_%s.md", strings.TrimPrefix(token, "wiki:"))
		if err := writeFileIfChanged(filepath.Join(refDir, name), []byte(content)); err != nil {
			slog.WarnContext(ctx, "[skill/lark_doc] failed to write reference", "token", token, "error", err)
		}
	}
	return nil
}

// convertDocToMarkdown fetches a document and renders it as markdown, returning
// the tokens it links to. At depth > 0 the links are left as links instead of
// being collected, which is what stops the traversal.
func (b *larkDocBackend) convertDocToMarkdown(
	ctx context.Context, token string, depth int, blockCache map[string][]*larkdocx.Block,
) (string, map[string]bool, error) {
	blocks, ok := blockCache[token]
	if !ok {
		fetched, err := b.fetchDocBlocks(ctx, token)
		if err != nil {
			return "", nil, err
		}
		blocks = fetched
		blockCache[token] = blocks
	}

	blocksMap := make(map[string]*larkdocx.Block, len(blocks))
	rootID := ""
	for _, blk := range blocks {
		if blk == nil || blk.BlockId == nil {
			continue
		}
		blocksMap[*blk.BlockId] = blk
		if ptr.EqualTo(blk.BlockType, larkBlockPage) {
			rootID = *blk.BlockId
		}
	}
	if rootID == "" {
		rootID = token // a document whose page block id differs from its token
	}

	root := blocksMap[rootID]
	if root == nil {
		return "", nil, fmt.Errorf("lark_doc: no page block found for %s", token)
	}

	referenced := make(map[string]bool)
	var sb strings.Builder
	for _, childID := range root.Children {
		child, ok := blocksMap[childID]
		if !ok {
			continue
		}
		rendered := renderBlock(b, child, blocksMap, depth, referenced)
		if rendered == "" {
			continue
		}
		sb.WriteString(rendered)
		if !strings.HasSuffix(rendered, "\n") {
			sb.WriteString("\n")
		}
	}
	return sb.String(), referenced, nil
}

// fetchDocBlocks pages through every block of a document, resolving a wiki node
// to the document behind it first.
func (b *larkDocBackend) fetchDocBlocks(ctx context.Context, token string) ([]*larkdocx.Block, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("empty document token")
	}
	if strings.Contains(token, " ") {
		return nil, fmt.Errorf("invalid document token %q", token)
	}

	// A "wiki:" prefix means "resolve as a wiki node"; without it the token is
	// tried as a wiki node and falls back to a plain document id, because the
	// two are indistinguishable from the token alone.
	wikiToken := strings.TrimPrefix(token, "wiki:")
	if resolved, err := b.resolveWikiToken(ctx, wikiToken); err == nil {
		token = resolved
	} else if strings.HasPrefix(token, "wiki:") {
		return nil, fmt.Errorf("lark_doc: resolve wiki token %s: %w", wikiToken, err)
	} else {
		slog.DebugContext(ctx, "lark_doc: token is not a wiki node; using it as a document id",
			"token", token, "error", err)
	}

	var all []*larkdocx.Block
	var pageToken *string
	for {
		req := larkdocx.NewListDocumentBlockReqBuilder().DocumentId(token).PageSize(500)
		if pageToken != nil {
			req.PageToken(*pageToken)
		}
		resp, err := b.larkClient.Docx.DocumentBlock.List(ctx, req.Build())
		if err != nil {
			return nil, fmt.Errorf("lark_doc: list blocks: %w", err)
		}
		if !resp.Success() {
			return nil, fmt.Errorf("lark_doc: list blocks failed: code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data == nil || len(resp.Data.Items) == 0 {
			break
		}
		all = append(all, resp.Data.Items...)
		if resp.Data.HasMore == nil || !*resp.Data.HasMore {
			break
		}
		pageToken = resp.Data.PageToken
	}
	return all, nil
}

// resolveWikiToken turns a wiki node token into the document token behind it.
func (b *larkDocBackend) resolveWikiToken(ctx context.Context, wikiToken string) (string, error) {
	resp, err := b.larkClient.Wiki.V2.Space.GetNode(ctx,
		larkwiki.NewGetNodeSpaceReqBuilder().Token(wikiToken).Build())
	if err != nil {
		return "", fmt.Errorf("lark_doc: wiki get_node: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("lark_doc: wiki get_node failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.Node == nil {
		return "", fmt.Errorf("lark_doc: wiki get_node returned no node")
	}
	node := resp.Data.Node
	if !ptr.EqualTo(node.ObjType, "docx") {
		return "", fmt.Errorf("lark_doc: unsupported wiki node type %v (only docx)", node.ObjType)
	}
	if ptr.From(node.ObjToken) == "" {
		return "", fmt.Errorf("lark_doc: wiki node has no obj_token")
	}
	return *node.ObjToken, nil
}

// extractDocToken pulls a document token out of a Feishu URL, or returns empty
// for a URL that is not a Feishu document.
//
// The domain check is what keeps an ordinary external link — github.com/…,
// example.com/… — from being mistaken for a document reference whose "token"
// happens to be the last path segment.
func extractDocToken(rawURL string) string {
	// Some clients emit already-percent-encoded hrefs, which hide the "/"
	// separators and the "/wiki/" marker from the checks below.
	if decoded, err := url.QueryUnescape(rawURL); err == nil && decoded != rawURL {
		rawURL = decoded
	}
	if (strings.Contains(rawURL, ".") || strings.Contains(rawURL, "://")) &&
		!strings.Contains(rawURL, "feishu.cn") && !strings.Contains(rawURL, "larksuite.com") {
		return ""
	}
	if idx := strings.IndexAny(rawURL, "?#"); idx != -1 {
		rawURL = rawURL[:idx]
	}
	rawURL = strings.TrimRight(rawURL, "/")
	if idx := strings.LastIndex(rawURL, "/"); idx != -1 {
		return strings.TrimSpace(rawURL[idx+1:])
	}
	return strings.TrimSpace(rawURL)
}

// Filesystem and shell operations are served by the local backend that holds
// the persisted document tree.

func (b *larkDocBackend) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	return b.local.LsInfo(ctx, req)
}

func (b *larkDocBackend) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	return b.local.Read(ctx, req)
}

func (b *larkDocBackend) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	return b.local.GrepRaw(ctx, req)
}

func (b *larkDocBackend) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	return b.local.GlobInfo(ctx, req)
}

func (b *larkDocBackend) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	return b.local.Write(ctx, req)
}

func (b *larkDocBackend) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	return b.local.Edit(ctx, req)
}

func (b *larkDocBackend) Execute(ctx context.Context, req *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	return b.local.Execute(ctx, req)
}

func (b *larkDocBackend) ExecuteStreaming(
	ctx context.Context, req *filesystem.ExecuteRequest,
) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	return b.local.ExecuteStreaming(ctx, req)
}
