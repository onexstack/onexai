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
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// toolSanitizer classifies how a tool's detail text is sanitized for display.
type toolSanitizer string

const (
	toolSanitizerGeneric toolSanitizer = "generic"
	toolSanitizerSkill   toolSanitizer = "skill"
	toolSanitizerPath    toolSanitizer = "path"
	toolSanitizerSearch  toolSanitizer = "search"
	toolSanitizerURL     toolSanitizer = "url"
	toolSanitizerCommand toolSanitizer = "command"
)

const (
	// defaultToolIcon is the icon token used when no descriptor matches a tool.
	defaultToolIcon = "app-default_outlined"
	// reasoningToolIcon is the icon token for thinking/reasoning steps.
	reasoningToolIcon = "mindmap_outlined"
)

// toolDescriptor maps a tool name to its display title, icon and detail
// sanitizer, plus the parameter keys and summary patterns used to extract a
// concise human-readable detail from the tool's input.
type toolDescriptor struct {
	Aliases         []string
	IconToken       string
	Title           string
	Sanitizer       toolSanitizer
	ParamKeys       []string
	SummaryPatterns []*regexp.Regexp
}

// toolDisplay is the resolved display metadata for a single tool invocation.
type toolDisplay struct {
	Title     string
	IconToken string
	Detail    string
}

var toolDescriptors = []toolDescriptor{
	{Aliases: []string{"skill"}, IconToken: "app-default_outlined", Title: "Load skill", Sanitizer: toolSanitizerSkill, ParamKeys: []string{"skill", "name"}, SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:load|use)\s+skill\s+(.+)$`)}},
	{Aliases: []string{"read", "open"}, IconToken: "file-link-text_outlined", Title: "Read", Sanitizer: toolSanitizerPath, ParamKeys: []string{"file_path", "path", "file"}, SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:read|open)\s+(?:file\s+)?(.+)$`)}},
	{Aliases: []string{"write", "edit", "patch", "apply_patch", "file_change", "filechange"}, IconToken: "edit_outlined", Title: "Edit", Sanitizer: toolSanitizerPath, ParamKeys: []string{"file_path", "path", "file"}, SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:edit|write|patch)\s+(?:file\s+)?(.+)$`)}},
	{Aliases: []string{"tool_search", "tool_search_tool", "find_tools", "search_tools"}, IconToken: "search_outlined", Title: "Search tools", Sanitizer: toolSanitizerSearch, ParamKeys: []string{"query", "q"}},
	{Aliases: []string{"web_search", "websearch", "web-search", "web.run", "web_run", "search"}, IconToken: "search_outlined", Title: "Search web", Sanitizer: toolSanitizerSearch, ParamKeys: []string{"query", "q"}, SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:search\s+(?:web\s+)?(?:for|about)|query)\s+(.+)$`)}},
	{Aliases: []string{"web_fetch", "webfetch", "web-fetch", "fetch"}, IconToken: "language_outlined", Title: "Fetch web page", Sanitizer: toolSanitizerURL, ParamKeys: []string{"url"}, SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:fetch|open)\s+(?:web\s+page\s+)?(?:from\s+)?(.+)$`)}},
	{Aliases: []string{"grep"}, IconToken: "doc-search_outlined", Title: "Search text", Sanitizer: toolSanitizerGeneric, ParamKeys: []string{"pattern"}, SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:search\s+text(?:\s+by\s+pattern)?|grep)\s+(.+)$`)}},
	{Aliases: []string{"glob", "file_search", "filesearch"}, IconToken: "folder_outlined", Title: "Search files", Sanitizer: toolSanitizerGeneric, ParamKeys: []string{"pattern", "query"}, SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:search\s+files(?:\s+by\s+pattern)?|glob)\s+(.+)$`)}},
	{Aliases: []string{"write_stdin", "stdin", "command_input"}, IconToken: "keyboard_outlined", Title: "Command I/O", Sanitizer: toolSanitizerCommand, ParamKeys: []string{"chars"}},
	{Aliases: []string{"exec", "exec_command", "bash", "shell", "run_shell_command", "command", "run"}, IconToken: "command_outlined", Title: "Run command", Sanitizer: toolSanitizerCommand, ParamKeys: []string{"cmd", "command", "script", "description"}, SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:run|execute)\s+(?:command|script)?\s*(.+)$`)}},
	{Aliases: []string{"browser", "playwright", "navigate"}, IconToken: "browser-mac_outlined", Title: "Browser", Sanitizer: toolSanitizerURL, ParamKeys: []string{"url"}, SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:open|browse|visit|navigate\s+to)\s+(.+)$`)}},
	{Aliases: []string{"agent", "task", "spawn", "spawn_agent", "wait_agent", "close_agent", "send_input", "resume_agent"}, IconToken: "robot_outlined", Title: "Run sub-agent", Sanitizer: toolSanitizerGeneric, ParamKeys: []string{"task", "description", "prompt"}, SummaryPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)^(?:run\s+sub-?agent|spawn\s+agent)\s+(.+)$`)}},
	{Aliases: []string{"multi_tool_use", "multi_tool_use.parallel", "parallel"}, IconToken: "list-check_outlined", Title: "Run tools", Sanitizer: toolSanitizerGeneric, ParamKeys: []string{"description", "prompt"}},
	{Aliases: []string{"update_plan", "plan_update"}, IconToken: "list-check_outlined", Title: "Update plan", Sanitizer: toolSanitizerGeneric, ParamKeys: []string{"target", "subject", "description"}},
	{Aliases: []string{"check", "determine", "verify", "todowrite", "todo_write"}, IconToken: "list-check_outlined", Title: "Check", Sanitizer: toolSanitizerGeneric, ParamKeys: []string{"target", "subject", "description"}},
	{Aliases: []string{"summarize", "analyze", "prepare"}, IconToken: "report_outlined", Title: "Analyze", Sanitizer: toolSanitizerGeneric, ParamKeys: []string{"target", "subject", "description"}},
	{Aliases: []string{"mcp", "mcp_tool", "mcptool"}, IconToken: "code_outlined", Title: "MCP tool", Sanitizer: toolSanitizerGeneric, ParamKeys: []string{"tool", "name", "server"}},
	{Aliases: []string{"permissions", "permission"}, IconToken: "safe-settings_outlined", Title: "Permissions", Sanitizer: toolSanitizerGeneric},
	{Aliases: []string{"computer_use", "computeruse"}, IconToken: "robot_outlined", Title: "Computer use", Sanitizer: toolSanitizerGeneric},
	{Aliases: []string{"code_interpreter", "codeinterpreter"}, IconToken: "code_outlined", Title: "Code interpreter", Sanitizer: toolSanitizerGeneric},
	{Aliases: []string{"ask_user_question", "askuserquestion", "request_user_input"}, IconToken: "robot_outlined", Title: "Ask user", Sanitizer: toolSanitizerGeneric},
	{Aliases: []string{"lsp"}, IconToken: "code_outlined", Title: "LSP", Sanitizer: toolSanitizerGeneric},
}

func normalizeToolNameForDisplay(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func compactToolNameForDisplay(name string) string {
	name = normalizeToolNameForDisplay(name)
	replacer := strings.NewReplacer("_", "", "-", "", " ", "", ".", "")
	return replacer.Replace(name)
}

func toolNameDisplayVariants(name string) []string {
	normalized := normalizeToolNameForDisplay(name)
	if normalized == "" {
		return nil
	}
	variants := []string{normalized}
	for _, sep := range []string{".", ":", "/"} {
		if idx := strings.LastIndex(normalized, sep); idx >= 0 && idx+1 < len(normalized) {
			variants = append(variants, normalized[idx+1:])
		}
	}
	seen := make(map[string]struct{}, len(variants))
	out := variants[:0]
	for _, variant := range variants {
		if variant == "" {
			continue
		}
		if _, ok := seen[variant]; ok {
			continue
		}
		seen[variant] = struct{}{}
		out = append(out, variant)
	}
	return out
}

func toolDescriptorMatches(desc toolDescriptor, toolName string, allowPrefix bool) bool {
	variants := toolNameDisplayVariants(toolName)
	for _, alias := range desc.Aliases {
		alias = normalizeToolNameForDisplay(alias)
		aliasCompact := compactToolNameForDisplay(alias)
		for _, variant := range variants {
			if variant == alias {
				return true
			}
			compact := compactToolNameForDisplay(variant)
			if compact == aliasCompact {
				return true
			}
			if allowPrefix && (strings.HasPrefix(variant, alias+"_") || strings.HasPrefix(variant, alias+"-") || strings.HasPrefix(compact, aliasCompact)) {
				return true
			}
		}
	}
	return false
}

func resolveToolDescriptor(toolName string) *toolDescriptor {
	for i := range toolDescriptors {
		if toolDescriptorMatches(toolDescriptors[i], toolName, false) {
			return &toolDescriptors[i]
		}
	}
	for i := range toolDescriptors {
		if toolDescriptorMatches(toolDescriptors[i], toolName, true) {
			return &toolDescriptors[i]
		}
	}
	return nil
}

func humanizeToolName(toolName string) string {
	name := strings.TrimSpace(toolName)
	if name == "" {
		return "Tool"
	}
	name = strings.NewReplacer("_", " ", "-", " ").Replace(name)
	words := strings.Fields(name)
	if len(words) == 0 {
		return "Tool"
	}
	for i, word := range words {
		if word == strings.ToUpper(word) {
			continue
		}
		lower := strings.ToLower(word)
		words[i] = strings.ToUpper(lower[:1]) + lower[1:]
	}
	return strings.Join(words, " ")
}

func buildToolDisplay(toolName, detail string) toolDisplay {
	desc := resolveToolDescriptor(toolName)
	title := humanizeToolName(toolName)
	icon := defaultToolIcon
	sanitizer := toolSanitizerGeneric
	if desc != nil {
		title = desc.Title
		icon = desc.IconToken
		sanitizer = desc.Sanitizer
	}

	rawDetail := strings.TrimSpace(detail)
	if desc != nil {
		if extracted := extractToolDetailFromJSON(rawDetail, *desc); extracted != "" {
			rawDetail = extracted
		} else if extracted := extractToolDetailFromSummary(rawDetail, *desc); extracted != "" {
			rawDetail = extracted
		}
	}
	cleanDetail := sanitizeToolDetail(sanitizer, rawDetail)
	if desc != nil && desc.Title == "Read" && isSkillPathValue(cleanDetail) {
		title = "Skill Read"
	}
	if desc != nil && desc.Title == "Run command" {
		if commandTitle, commandIcon, ok := classifyCommandToolDetail(cleanDetail); ok {
			title = commandTitle
			icon = commandIcon
		}
	}
	return toolDisplay{Title: title, IconToken: icon, Detail: cleanDetail}
}

func classifyCommandToolDetail(detail string) (title, icon string, ok bool) {
	command := strings.ToLower(stripToolDisplayQuotes(detail))
	if command == "" {
		return "", "", false
	}
	if strings.HasPrefix(command, "git ") || command == "git" {
		return "Git", "code_outlined", true
	}
	if strings.HasPrefix(command, "gh ") || command == "gh" {
		return "GitHub", "cloud_outlined", true
	}
	if commandHasAnyPrefix(command, "go test", "npm test", "npm run test", "pnpm test", "yarn test", "pytest", "cargo test", "swift test", "xcodebuild test") {
		return "Run tests", "list-check_outlined", true
	}
	if commandHasAnyPrefix(command, "make test", "go build", "make build", "npm run build", "pnpm build", "yarn build", "cargo build", "swift build", "xcodebuild build") {
		return "Build", "codeblock_outlined", true
	}
	if commandHasAnyPrefix(command, "ps ", "kill ", "pkill ", "launchctl ", "lsof ") {
		return "Inspect process", "computer_outlined", true
	}
	if commandHasAnyPrefix(command, "rg ", "grep ", "ag ", "ack ") || strings.Contains(command, " | grep ") || strings.Contains(command, " | rg ") {
		return "Search text", "doc-search_outlined", true
	}
	if commandHasAnyPrefix(command, "ls ", "stat ", "pwd", "cat ", "sed ", "awk ", "head ", "tail ", "wc ", "strings ", "find ") {
		return "Inspect files", "file-link-text_outlined", true
	}
	if commandHasAnyPrefix(command, "curl ", "wget ", "ssh ", "scp ", "rsync ") {
		return "Network", "cloud_outlined", true
	}
	if commandHasAnyPrefix(command, "cp ", "mv ", "rm ", "mkdir ", "rmdir ", "chmod ", "chown ", "touch ") {
		return "File operation", "folder_outlined", true
	}
	if commandHasAnyPrefix(command, "sleep ", "osascript -e 'delay ", "osascript -e \"delay ") {
		return "Wait", "alarm-clock_outlined", true
	}
	return "", "", false
}

func commandHasAnyPrefix(command string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

func extractToolDetailFromJSON(text string, desc toolDescriptor) string {
	text = strings.TrimSpace(text)
	if text == "" || len(desc.ParamKeys) == 0 {
		return ""
	}
	candidates := []string{text}
	if idx := strings.Index(text, "{"); idx > 0 {
		candidates = append(candidates, text[idx:])
	}
	for _, candidate := range candidates {
		var params map[string]any
		if err := json.Unmarshal([]byte(candidate), &params); err != nil {
			continue
		}
		if len(params) == 0 {
			continue
		}
		if desc.Title == "Search text" {
			if pattern := extractScalarText(params["pattern"]); pattern != "" {
				if target := extractScalarText(params["glob"]); target != "" {
					return pattern + " in " + target
				}
				if target := extractScalarText(params["path"]); target != "" {
					return pattern + " in " + target
				}
				if target := extractScalarText(params["file_path"]); target != "" {
					return pattern + " in " + target
				}
				return pattern
			}
		}
		for _, key := range desc.ParamKeys {
			if value := extractScalarText(params[key]); value != "" {
				return value
			}
		}
	}
	return ""
}

func extractScalarText(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

func extractToolDetailFromSummary(text string, desc toolDescriptor) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(stripToolDisplayMarkdown(line))
		if line == "" {
			continue
		}
		if desc.Sanitizer == toolSanitizerURL {
			if urlText := extractFirstURL(line); urlText != "" {
				return urlText
			}
		}
		for _, pattern := range desc.SummaryPatterns {
			if match := pattern.FindStringSubmatch(line); len(match) > 1 {
				return strings.TrimSpace(match[1])
			}
		}
		if code := extractFirstCodeSpan(line); code != "" {
			return code
		}
		if quoted := extractFirstQuotedText(line); quoted != "" {
			return quoted
		}
		return line
	}
	return ""
}

func stripToolDisplayMarkdown(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "- ")
	text = strings.TrimPrefix(text, "* ")
	text = strings.Trim(text, "`")
	text = strings.Trim(text, "_")
	return strings.TrimSpace(text)
}

// credentialName matches the key names that make a value a credential.
//
// It is one string rather than an alternation repeated in each pattern below,
// because the patterns are the spellings of the same idea — an assignment, a
// JSON member, an auth header — and a name added to one and forgotten in another
// is a secret that survives on one path. Deliberately broad: it matches a name
// that merely contains one of these words, so `token_count` is treated as a
// credential name too. Naming a value after a credential and expecting it to be
// printed is the case worth losing.
const credentialName = `(?:token|secret|password|api[_-]?key|authorization|cookie|credential|bearer|session[_-]?id|client[_-]?secret|access[_-]?key)`

var (
	firstURLRe        = regexp.MustCompile(`https?://[^\s'"` + "`" + `<>]+`)
	firstCodeSpanRe   = regexp.MustCompile("`([^`]+)`")
	firstQuotedTextRe = regexp.MustCompile(`"([^"]+)"|'([^']+)'`)
	secretAssignRe    = regexp.MustCompile(`(?i)\b([A-Za-z_][A-Za-z0-9_]*` + credentialName + `[A-Za-z0-9_]*)=("[^"]*"|'[^']*'|[^\s"'` + "`" + `]+)`)
	// jsonSecretRe is the same assignment in JSON, which is how most tool output
	// is shaped: the key is quoted and the separator is a colon, so the pattern
	// above never matches it. A credential echoed by a command that reports in
	// JSON would otherwise travel intact.
	jsonSecretRe    = regexp.MustCompile(`(?i)("[A-Za-z0-9_]*` + credentialName + `[A-Za-z0-9_]*"\s*:\s*)("[^"]*"|[^,}\s]+)`)
	authHeaderRe    = regexp.MustCompile(`(?i)(Authorization\s*:\s*(?:Bearer|Basic|Token)\s+)([^\s'"` + "`" + `]+)`)
	sensitiveNameRe = regexp.MustCompile(`(?i)` + credentialName)
)

func extractFirstURL(text string) string { return firstURLRe.FindString(text) }

func extractFirstCodeSpan(text string) string {
	if match := firstCodeSpanRe.FindStringSubmatch(text); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func extractFirstQuotedText(text string) string {
	if match := firstQuotedTextRe.FindStringSubmatch(text); len(match) > 2 {
		if strings.TrimSpace(match[1]) != "" {
			return strings.TrimSpace(match[1])
		}
		return strings.TrimSpace(match[2])
	}
	return ""
}

func sanitizeToolDetail(kind toolSanitizer, value string) string {
	cleaned := sanitizeGenericToolText(value)
	switch kind {
	case toolSanitizerSkill:
		cleaned = regexp.MustCompile(`(?i)^skill\s+`).ReplaceAllString(cleaned, "")
		cleaned = strings.NewReplacer("-", " ", "_", " ").Replace(cleaned)
		return strings.TrimSpace(cleaned)
	case toolSanitizerSearch:
		return stripToolDisplayQuotes(cleaned)
	case toolSanitizerURL:
		return sanitizeURLText(stripToolDisplayQuotes(cleaned))
	case toolSanitizerCommand:
		return sanitizeCommandLike(cleaned)
	case toolSanitizerPath:
		return redactInlineSecrets(cleaned)
	default:
		return redactInlineSecrets(cleaned)
	}
}

func sanitizeGenericToolText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return redactInlineSecrets(value)
}

func stripToolDisplayQuotes(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	return value
}

func sanitizeURLText(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "from "))
	if value == "" {
		return ""
	}
	if u, err := url.Parse(value); err == nil && u.Scheme != "" && u.Host != "" {
		u.User = nil
		q := u.Query()
		for key := range q {
			if sensitiveNameRe.MatchString(key) {
				q.Set(key, "[redacted]")
			}
		}
		u.RawQuery = q.Encode()
		return u.String()
	}
	return redactInlineSecrets(value)
}

func sanitizeCommandLike(value string) string {
	value = stripToolDisplayQuotes(value)
	value = regexp.MustCompile(`(?i)^(?:command|script|description)\s+`).ReplaceAllString(value, "")
	return redactInlineSecrets(value)
}

// RedactInlineSecrets masks secrets that appear inline in free-form text:
// `token=...`-style assignments and `Authorization: Bearer ...`-style headers.
//
// It is exported because the MCP surface needs the same treatment for a
// different reason than the card surface does. A card is rendered for the user
// who asked; a tool result is handed to a model, and the model here runs
// outside this host. Text that reaches it has left the building, so a secret
// echoed by a command has to be masked on the way out, not only on display.
//
// The two patterns it looks for are deliberately narrow — a key name that names
// a credential, or an auth header — so ordinary output (a job listing, a
// pipeline URL) passes through unchanged.
func RedactInlineSecrets(value string) string {
	return redactInlineSecrets(value)
}

// argumentsDigestLimit is how much of a call's arguments fit on the line that
// asks for a decision. The line is one line of a log that may hold several
// decisions; the point is to say what the call is about, not to reproduce it,
// and a JSON body past this length is one the reader would have to copy out and
// reformat anyway.
const argumentsDigestLimit = 80

// ArgumentsDigest renders the arguments of a pending call as the short
// parenthetical its decision line carries: `（env=edu-onex pipeline=content）`.
//
// It is a digest rather than the JSON because of where it appears. The line it
// goes on sits in a log of decisions above the buttons, next to a tool name, and
// the reader is deciding whether to let a call run — which the top-level scalars
// answer. Nesting is summarized rather than expanded: a value that is itself an
// object prints as {}, because a decision line with a nested document in it is
// one nobody reads, and the full call is on the card's own question anyway.
//
// An empty return means "say nothing": no arguments, or nothing that could be
// said in the space. Every caller appends it directly to a tool name. A call
// whose arguments are the empty object — or JSON null, or an object whose every
// member was empty — has nothing to say here, and printing `（null）` or `（{ }）`
// for it would put a word on the line that names no argument of the call.
func ArgumentsDigest(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return ""
	}
	digest, isObject := digestArgumentObject(args)
	if !isObject {
		// Not an object — a permission whose call reported something else, or
		// malformed JSON. Said as it stands, whitespace collapsed, rather than
		// dropped: an approval that shows nothing invites approving nothing.
		digest = strings.Join(strings.Fields(args), " ")
	}
	digest = compactRichText(RedactInlineSecrets(digest), argumentsDigestLimit)
	if digest == "" {
		return ""
	}
	return "（" + digest + "）"
}

// digestArgumentObject renders a JSON object's scalar members as key=value
// pairs, in key order so that the same call always reads the same way. Empty
// members are dropped, so a call that sets only some of a tool's fields does not
// pad the line with the ones it left unset.
//
// isObject distinguishes "this parsed as an object and rendered to nothing" from
// "this is not an object at all": the first means the call takes no arguments,
// the second means the caller should show the text as it stands. Reporting both
// as an empty string would make the two indistinguishable.
func digestArgumentObject(args string) (digest string, isObject bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(args), &obj); err != nil {
		return "", false
	}
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := digestArgumentValue(obj[key])
		if value == "" {
			continue
		}
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, " "), true
}

// digestArgumentValue renders one member of a call's arguments. A nested object
// becomes {}, a list becomes its members joined by commas, and anything else is
// printed as it stands.
func digestArgumentValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			member := digestArgumentValue(item)
			if member != "" && member != "{}" {
				parts = append(parts, member)
			}
		}
		return strings.Join(parts, ",")
	case map[string]any:
		return "{}"
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func redactInlineSecrets(value string) string {
	value = secretAssignRe.ReplaceAllString(value, "$1=[redacted]")
	// The JSON form keeps its quotes: the replacement is spliced into a document
	// that is still JSON, and dropping them would leave it unparseable to a
	// reader who copies it out.
	value = jsonSecretRe.ReplaceAllString(value, `$1"[redacted]"`)
	value = authHeaderRe.ReplaceAllString(value, "$1[redacted]")
	return value
}

func isSkillPathValue(value string) bool {
	return strings.Contains(strings.ToLower(value), "/skills/")
}
