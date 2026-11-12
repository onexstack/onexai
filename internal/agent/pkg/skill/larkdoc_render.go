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

package skill

import (
	"fmt"
	"strings"

	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	"github.com/onexstack/onexstack/pkg/ptr"
	"gopkg.in/yaml.v3"
)

// Feishu document block types this renderer understands. They are the wire
// values from the docx API, which is why they are bare numbers rather than a
// named enum.
const (
	larkBlockPage          = 1
	larkBlockText          = 2
	larkBlockHeading1      = 3
	larkBlockHeading9      = 11
	larkBlockBullet        = 12
	larkBlockOrdered       = 13
	larkBlockCode          = 14
	larkBlockQuote         = 15
	larkBlockTodo          = 17
	larkBlockDivider       = 22
	larkBlockTable         = 31
	larkWikiObjTypeDocx    = 16 // MentionDoc.ObjType for a wiki-hosted document
	larkHeadingCount       = 9  // Heading1..Heading9 are block types 3..11
	larkTableSeparatorCell = "---"
)

// isSkillDocument reports whether a converted document declares itself a skill,
// and returns its front matter when it does.
//
// A document is a skill only when its front matter says "type: skill". That
// marker is what distinguishes an agent's skill from any other page in the same
// wiki, so a page that merely looks structured is never picked up by accident.
func isSkillDocument(data string) (bool, adkskill.FrontMatter) {
	frontmatter, ok := extractFrontmatter(data)
	if !ok {
		return false, adkskill.FrontMatter{}
	}
	normalized := strings.ReplaceAll(strings.ToLower(frontmatter), " ", "")
	if !strings.Contains(normalized, "type:skill") {
		return false, adkskill.FrontMatter{}
	}
	var fm adkskill.FrontMatter
	if err := yaml.Unmarshal([]byte(frontmatter), &fm); err != nil || fm.Name == "" {
		return false, adkskill.FrontMatter{}
	}
	return true, fm
}

// extractFrontmatter pulls the YAML block off the front of a converted
// document. It accepts both a fenced ```yaml block, which is how a wiki page
// usually carries it, and a plain "---" delimited block.
func extractFrontmatter(data string) (string, bool) {
	data = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(data), "\ufeff"))

	if strings.HasPrefix(data, "```") {
		firstNewline := strings.Index(data, "\n")
		if firstNewline == -1 {
			return "", false
		}
		rest := data[firstNewline+1:]
		closeIdx := strings.Index(rest, "```")
		if closeIdx == -1 {
			return "", false
		}
		block := strings.TrimSpace(rest[:closeIdx])
		if strings.HasPrefix(block, "---") {
			block = strings.TrimPrefix(block, "---")
			if idx := strings.Index(block, "---"); idx != -1 {
				block = block[:idx]
			}
		}
		return strings.TrimSpace(block), true
	}

	const delimiter = "---"
	if !strings.HasPrefix(data, delimiter) {
		return "", false
	}
	rest := data[len(delimiter):]
	endIdx := strings.Index(rest, "\n"+delimiter)
	if endIdx == -1 {
		endIdx = strings.Index(rest, delimiter)
	}
	if endIdx == -1 {
		return "", false
	}
	return strings.TrimSpace(rest[:endIdx]), true
}

// renderBlock translates one block into markdown.
//
// referencedTokens collects the documents this block links to, but only at
// depth 0: a reference's own references are rendered as ordinary links, which
// is what bounds a document graph that contains a cycle.
func renderBlock(
	b *larkDocBackend, block *larkdocx.Block,
	blocksMap map[string]*larkdocx.Block, depth int, referencedTokens map[string]bool,
) string {
	if block == nil || block.BlockType == nil {
		return ""
	}
	switch bt := *block.BlockType; {
	case bt == larkBlockPage && block.Page != nil:
		return renderText(b, block.Page, depth, referencedTokens)
	case bt == larkBlockText && block.Text != nil:
		return renderText(b, block.Text, depth, referencedTokens) + "\n"
	case bt >= larkBlockHeading1 && bt <= larkBlockHeading9:
		return renderHeading(b, block, bt, depth, referencedTokens)
	case bt == larkBlockBullet && block.Bullet != nil:
		return "- " + renderText(b, block.Bullet, depth, referencedTokens) + "\n"
	case bt == larkBlockOrdered && block.Ordered != nil:
		// Markdown numbers ordered items itself, so "1." is correct for each.
		return "1. " + renderText(b, block.Ordered, depth, referencedTokens) + "\n"
	case bt == larkBlockCode && block.Code != nil:
		return "```\n" + renderText(b, block.Code, depth, referencedTokens) + "\n```\n"
	case bt == larkBlockQuote && block.Quote != nil:
		return renderQuote(b, block.Quote, depth, referencedTokens)
	case bt == larkBlockTodo && block.Todo != nil:
		return "- [ ] " + renderText(b, block.Todo, depth, referencedTokens) + "\n"
	case bt == larkBlockDivider:
		return "---\n"
	case bt == larkBlockTable && block.Table != nil:
		return renderTable(b, block, blocksMap, depth, referencedTokens)
	}
	return ""
}

// renderHeading renders Heading1..Heading9 as the matching markdown level.
func renderHeading(
	b *larkDocBackend, block *larkdocx.Block, blockType int,
	depth int, referencedTokens map[string]bool,
) string {
	text := headingContent(block, blockType)
	if text == nil {
		return ""
	}
	level := blockType - larkBlockHeading1 + 1
	return strings.Repeat("#", level) + " " + renderText(b, text, depth, referencedTokens) + "\n"
}

// headingContent returns the text payload of a heading block.
func headingContent(block *larkdocx.Block, blockType int) *larkdocx.Text {
	headings := []*larkdocx.Text{
		block.Heading1, block.Heading2, block.Heading3, block.Heading4, block.Heading5,
		block.Heading6, block.Heading7, block.Heading8, block.Heading9,
	}
	idx := blockType - larkBlockHeading1
	if idx < 0 || idx >= len(headings) {
		return nil
	}
	return headings[idx]
}

// renderQuote prefixes every line of a quote block with "> ".
func renderQuote(
	b *larkDocBackend, text *larkdocx.Text, depth int, referencedTokens map[string]bool,
) string {
	var sb strings.Builder
	for _, line := range strings.Split(renderText(b, text, depth, referencedTokens), "\n") {
		sb.WriteString("> " + line + "\n")
	}
	return sb.String()
}

// renderTable renders a table block with a markdown header separator after its
// first row.
func renderTable(
	b *larkDocBackend, block *larkdocx.Block,
	blocksMap map[string]*larkdocx.Block, depth int, referencedTokens map[string]bool,
) string {
	prop := block.Table.Property
	if prop == nil || prop.RowSize == nil || prop.ColumnSize == nil {
		return ""
	}
	rows, cols := *prop.RowSize, *prop.ColumnSize

	var sb strings.Builder
	for r := 0; r < rows; r++ {
		cells := make([]string, 0, cols)
		for c := 0; c < cols; c++ {
			idx := r*cols + c
			if idx >= len(block.Table.Cells) {
				cells = append(cells, "")
				continue
			}
			// A literal "|" inside a cell would be read as a column boundary.
			text := strings.ReplaceAll(renderCellText(b, block.Table.Cells[idx], blocksMap, depth, referencedTokens), "|", `\|`)
			cells = append(cells, text)
		}
		sb.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		if r == 0 {
			separators := make([]string, cols)
			for c := range separators {
				separators[c] = larkTableSeparatorCell
			}
			sb.WriteString("| " + strings.Join(separators, " | ") + " |\n")
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

// renderCellText joins a table cell's paragraphs with an HTML break, since a
// markdown newline inside a table row would end the row.
func renderCellText(
	b *larkDocBackend, cellID string,
	blocksMap map[string]*larkdocx.Block, depth int, referencedTokens map[string]bool,
) string {
	cell, ok := blocksMap[cellID]
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, childID := range cell.Children {
		child, ok := blocksMap[childID]
		if !ok {
			continue
		}
		text := renderBlockTextOnly(b, child, depth, referencedTokens)
		if text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("<br>")
		}
		sb.WriteString(text)
	}
	return sb.String()
}

// renderBlockTextOnly renders the block types that carry plain text, for use
// inside a container such as a table cell.
func renderBlockTextOnly(
	b *larkDocBackend, block *larkdocx.Block, depth int, referencedTokens map[string]bool,
) string {
	if block == nil || block.BlockType == nil {
		return ""
	}
	switch *block.BlockType {
	case larkBlockText:
		return renderText(b, block.Text, depth, referencedTokens)
	case larkBlockBullet:
		return renderText(b, block.Bullet, depth, referencedTokens)
	case larkBlockOrdered:
		return renderText(b, block.Ordered, depth, referencedTokens)
	case larkBlockTodo:
		return renderText(b, block.Todo, depth, referencedTokens)
	}
	return ""
}

// renderText formats rich text runs into markdown, and records the documents
// the text links to.
func renderText(b *larkDocBackend, text *larkdocx.Text, depth int, referencedTokens map[string]bool) string {
	if text == nil || len(text.Elements) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, el := range text.Elements {
		switch {
		case el == nil:
			continue
		case el.TextRun != nil:
			sb.WriteString(renderTextRun(b, el.TextRun, depth, referencedTokens))
		case el.MentionDoc != nil:
			sb.WriteString(renderMentionDoc(b, el.MentionDoc, depth, referencedTokens))
		}
	}
	return sb.String()
}

// renderTextRun renders one styled run.
func renderTextRun(
	b *larkDocBackend, run *larkdocx.TextRun, depth int, referencedTokens map[string]bool,
) string {
	if run.Content == nil {
		return ""
	}
	txt := *run.Content
	style := run.TextElementStyle
	if style == nil {
		return txt
	}
	if style.Link != nil && style.Link.Url != nil {
		url := ptr.From(style.Link.Url)
		if token := extractDocToken(url); token != "" {
			return b.documentLink(txt, token, url, depth, referencedTokens)
		}
		txt = fmt.Sprintf("[%s](%s)", txt, url)
	}
	if ptr.From(style.Bold) {
		txt = "**" + txt + "**"
	}
	if ptr.From(style.Italic) {
		txt = "*" + txt + "*"
	}
	if ptr.From(style.InlineCode) {
		txt = "`" + txt + "`"
	}
	if ptr.From(style.Strikethrough) {
		txt = "~~" + txt + "~~"
	}
	return txt
}

// renderMentionDoc renders an inline reference to another document.
func renderMentionDoc(
	b *larkDocBackend, mDoc *larkdocx.MentionDoc, depth int, referencedTokens map[string]bool,
) string {
	title := ptr.From(mDoc.Title)
	if mDoc.Token == nil {
		if mDoc.Url != nil {
			return fmt.Sprintf("[%s](%s)", title, *mDoc.Url)
		}
		return title
	}
	token := *mDoc.Token
	if isWikiMention(mDoc) {
		token = "wiki:" + token
	}
	return b.documentLink(title, token, ptr.From(mDoc.Url), depth, referencedTokens)
}

// isWikiMention reports whether a mentioned document lives in a wiki, which is
// the case that needs the "wiki:" prefix when it is resolved later.
func isWikiMention(mDoc *larkdocx.MentionDoc) bool {
	if strings.Contains(ptr.From(mDoc.Url), "/wiki/") {
		return true
	}
	return ptr.EqualTo(mDoc.ObjType, larkWikiObjTypeDocx)
}

// documentLink renders a link to another document. At depth 0 the target is
// recorded for download and linked to its local reference file; deeper, it
// stays an ordinary link so the traversal terminates.
func (b *larkDocBackend) documentLink(
	title, token, rawURL string, depth int, referencedTokens map[string]bool,
) string {
	if depth != 0 {
		if rawURL != "" {
			return fmt.Sprintf("[%s](%s)", title, rawURL)
		}
		return title
	}
	referencedTokens[token] = true
	return fmt.Sprintf("[%s](./references/referenced_%s.md)", title, strings.TrimPrefix(token, "wiki:"))
}
