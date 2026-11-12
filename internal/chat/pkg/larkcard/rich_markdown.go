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

package larkcard

import (
	"fmt"
	"regexp"
	"strings"
)

// escapeMarkdownText neutralizes the characters a tool name could carry into a
// row. A name comes from the descriptor table or from humanizeToolName — that is,
// from tool and MCP configuration rather than from a user — but the row is
// markdown now, and a name holding an asterisk or an angle bracket would style the
// row, or close the colour tag it sits in, instead of appearing in it. The
// backslashes are invisible once rendered.
func escapeMarkdownText(text string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"<", `\<`,
		">", `\>`,
		"\n", " ",
	).Replace(text)
}

var markdownTablePattern = regexp.MustCompile(`(?m)^\|.+\|\s*\n\|[\s:|-]+\|\s*\n(?:\|.+\|\s*\n?)+`)

// SplitMarkdownByTables splits markdown into parts each containing at most
// maxTables tables, so overflow tables can be sent as separate cards.
func SplitMarkdownByTables(md string, maxTables int) []string {
	if maxTables <= 0 {
		return []string{md}
	}
	matches := markdownTablePattern.FindAllStringIndex(md, -1)
	if len(matches) <= maxTables {
		return []string{md}
	}
	parts := make([]string, 0, len(matches)-maxTables+1)
	firstEnd := len(md)
	if len(matches) > maxTables {
		firstEnd = matches[maxTables][0]
	}
	first := strings.TrimSpace(md[:firstEnd])
	if first != "" {
		parts = append(parts, first)
	}
	for _, match := range matches[maxTables:] {
		block := strings.TrimSpace(md[match[0]:match[1]])
		if block != "" {
			parts = append(parts, block)
		}
	}
	return parts
}

var (
	mdLinkRe               = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	feishuCardImagePattern = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
)

// sanitizeCardMarkdownForCard preprocesses markdown for safe rendering in a
// Feishu card markdown element.
func sanitizeCardMarkdownForCard(text string) string {
	prepared := sanitizeMarkdownURLs(preprocessFeishuMarkdown(text))
	prepared = stripInvalidFeishuCardImages(prepared)
	prepared = optimizeFeishuCardMarkdown(prepared)
	prepared, _ = sanitizeCardMarkdownTables(prepared, richCardTableLimit)
	return prepared
}
func preprocessFeishuMarkdown(md string) string {
	var b strings.Builder
	b.Grow(len(md) + 32)
	for i := 0; i < len(md); i++ {
		if i > 0 && md[i] == '`' && i+2 < len(md) && md[i+1] == '`' && md[i+2] == '`' && md[i-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteByte(md[i])
	}
	return b.String()
}
func sanitizeMarkdownURLs(md string) string {
	var b strings.Builder
	last := 0
	for _, loc := range mdLinkRe.FindAllStringSubmatchIndex(md, -1) {
		if len(loc) < 6 {
			continue
		}
		start, end := loc[0], loc[1]
		if start > 0 && md[start-1] == '!' {
			continue
		}
		b.WriteString(md[last:start])
		text := md[loc[2]:loc[3]]
		href := md[loc[4]:loc[5]]
		match := md[start:end]
		if isValidFeishuHref(href) {
			b.WriteString(match)
		} else {
			b.WriteString(text + " (" + href + ")")
		}
		last = end
	}
	if last == 0 {
		return md
	}
	b.WriteString(md[last:])
	return b.String()
}
func isValidFeishuHref(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}
func stripInvalidFeishuCardImages(text string) string {
	if !strings.Contains(text, "![") {
		return text
	}
	return feishuCardImagePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := feishuCardImagePattern.FindStringSubmatch(match)
		if len(parts) == 3 && strings.HasPrefix(parts[2], "img_") {
			return match
		}
		return ""
	})
}
func optimizeFeishuCardMarkdown(text string) string {
	protected, blocks := protectFencedCodeBlocks(text)
	if regexp.MustCompile(`(?m)^#{1,3} `).MatchString(protected) {
		protected = regexp.MustCompile(`(?m)^#{2,6} (.+)$`).ReplaceAllString(protected, "##### $1")
		protected = regexp.MustCompile(`(?m)^# (.+)$`).ReplaceAllString(protected, "#### $1")
	}
	protected = regexp.MustCompile(`\n{3,}`).ReplaceAllString(protected, "\n\n")
	return restoreFencedCodeBlocks(protected, blocks)
}
func protectFencedCodeBlocks(text string) (string, []string) {
	var blocks []string
	var b strings.Builder
	for i := 0; i < len(text); {
		start := strings.Index(text[i:], "```")
		if start < 0 {
			b.WriteString(text[i:])
			break
		}
		start += i
		end := strings.Index(text[start+3:], "```")
		if end < 0 {
			b.WriteString(text[i:])
			break
		}
		end += start + 6
		b.WriteString(text[i:start])
		placeholder := fmt.Sprintf("\x00CC_FEISHU_CODE_BLOCK_%d\x00", len(blocks))
		blocks = append(blocks, text[start:end])
		b.WriteString(placeholder)
		i = end
	}
	return b.String(), blocks
}
func restoreFencedCodeBlocks(text string, blocks []string) string {
	for i, block := range blocks {
		placeholder := fmt.Sprintf("\x00CC_FEISHU_CODE_BLOCK_%d\x00", i)
		text = strings.ReplaceAll(text, placeholder, block)
	}
	return text
}
func sanitizeCardMarkdownTables(text string, remainingBudget int) (string, int) {
	matches := findMarkdownTablesOutsideCodeBlocks(text)
	if len(matches) <= remainingBudget {
		return text, remainingBudget - len(matches)
	}
	return wrapTablesBeyondLimit(text, matches, remainingBudget), 0
}
func wrapTablesBeyondLimit(text string, matches []markdownTextMatch, keepCount int) string {
	if len(matches) <= keepCount {
		return text
	}
	if keepCount < 0 {
		keepCount = 0
	}
	result := text
	for i := len(matches) - 1; i >= keepCount; i-- {
		match := matches[i]
		replacement := "```\n" + match.raw + "\n```"
		result = result[:match.start] + replacement + result[match.end:]
	}
	return result
}

type markdownTextMatch struct {
	start int
	end   int
	raw   string
}
type markdownLine struct {
	text       string
	start, end int
}

func findMarkdownTablesOutsideCodeBlocks(text string) []markdownTextMatch {
	lines := markdownLinesWithOffsets(text)
	var matches []markdownTextMatch
	inCodeBlock := false
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i].text)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock || i+1 >= len(lines) || !isMarkdownTableRow(lines[i].text) || !isMarkdownTableSeparator(lines[i+1].text) {
			continue
		}
		start := lines[i].start
		end := lines[i+1].end
		j := i + 2
		for j < len(lines) && isMarkdownTableRow(lines[j].text) {
			end = lines[j].end
			j++
		}
		matches = append(matches, markdownTextMatch{
			start: start,
			end:   end,
			raw:   strings.TrimSpace(text[start:end]),
		})
		i = j - 1
	}
	return matches
}
func markdownLinesWithOffsets(text string) []markdownLine {
	if text == "" {
		return nil
	}
	parts := strings.SplitAfter(text, "\n")
	lines := make([]markdownLine, 0, len(parts))
	offset := 0
	for _, part := range parts {
		if part == "" {
			continue
		}
		next := offset + len(part)
		lines = append(lines, markdownLine{text: part, start: offset, end: next})
		offset = next
	}
	return lines
}
func isMarkdownTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return len(trimmed) >= 2 && strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
}
func isMarkdownTableSeparator(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !isMarkdownTableRow(trimmed) {
		return false
	}
	hasDash := false
	for _, r := range trimmed {
		switch r {
		case '|', '-', ':', ' ':
			if r == '-' {
				hasDash = true
			}
		default:
			return false
		}
	}
	return hasDash
}
