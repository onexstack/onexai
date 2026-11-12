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

package agentbuilder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/schema"
)

// allowedExecutables is the closed set of commands the shell tool may run.
// Everything else is refused before the backend is reached. ripgrep is the
// only tool the skills workflow needs, and it is a pure search command, so a
// tighter list is strictly safer than a configurable one.
var allowedExecutables = []string{"rg", "ripgrep", "rigrep"}

// blockedPrefix marks every refusal produced by this backend. It is exported
// to the model as the tool result, so it must read as a refusal rather than a
// crash (which the error-conversion middleware would otherwise turn into a
// generic "tool execution failed").
const blockedPrefix = "【安全拦截】"

// SafeLocalBackend wraps the eino local filesystem backend to restrict every
// path operation to SkillsDir and every command to allowedExecutables. It is
// the sandbox the deep agent's read_file/write_file/edit_file/glob/grep/
// execute tools run behind.
type SafeLocalBackend struct {
	*localbk.Local
	SkillsDir string
}

// Path containment:

// scopedDir resolves a directory-shaped operation's path and verifies it is
// inside the sandbox.
//
// An empty path means "the sandbox root", not "wherever the process happens to
// be". The underlying backend does not read it that way — LsInfo would list the
// working directory, GrepRaw would search it, and GlobInfo would substitute "/"
// and walk the entire filesystem — so an empty path has to be turned into the
// root here, before the backend ever sees it.
func (s *SafeLocalBackend) scopedDir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = s.SkillsDir
	}
	if err := s.checkPath(path); err != nil {
		return "", err
	}
	return path, nil
}

// scopedFile resolves a file-shaped operation's path. An empty path is refused
// rather than resolved to the sandbox root: "the root" is not a file, and
// silently meaning a directory would be a worse answer than saying so.
func (s *SafeLocalBackend) scopedFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("文件路径不能为空")
	}
	if err := s.checkPath(path); err != nil {
		return "", err
	}
	return path, nil
}

// checkPath verifies that path resolves to a location strictly inside
// s.SkillsDir. Both sides have their symlinks resolved before comparison:
// resolving only one of them makes the check meaningless whenever any
// ancestor is a symlink.
func (s *SafeLocalBackend) checkPath(path string) error {
	if s.SkillsDir == "" {
		return fmt.Errorf("SkillsDir 未配置")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("获取绝对路径失败: %w", err)
	}
	skillsDirAbs, err := filepath.Abs(s.SkillsDir)
	if err != nil {
		return fmt.Errorf("获取 SkillsDir 绝对路径失败: %w", err)
	}

	absPath = resolveSymlinks(filepath.Clean(absPath))
	skillsDirAbs = resolveSymlinks(filepath.Clean(skillsDirAbs))

	rel, err := filepath.Rel(skillsDirAbs, absPath)
	if err != nil {
		return fmt.Errorf("计算相对路径失败: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("操作的文件或目录 %s 必须位于 %s 目录中", path, s.SkillsDir)
	}
	return nil
}

// resolveSymlinks resolves symlinks in path. filepath.EvalSymlinks fails
// outright when path does not exist, which is the normal case for a file
// about to be created, so walk up to the nearest existing ancestor, resolve
// that, and re-append the unresolved tail. Returns path unchanged if nothing
// resolves.
func resolveSymlinks(path string) string {
	current, tail := path, ""
	for {
		if eval, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(eval, tail)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path // reached the root without finding an existing ancestor
		}
		tail = filepath.Join(filepath.Base(current), tail)
		current = parent
	}
}

// Filesystem operations. Each one refuses by returning a model-visible value
// rather than an error wherever the eino interface allows it, so a blocked
// call reads as an answer the model can act on instead of a run-killing
// failure.

func blockedFileInfos(err error) []filesystem.FileInfo {
	return []filesystem.FileInfo{{Path: blockedPrefix + err.Error()}}
}

// LsInfo lists files, restricted to SkillsDir.
func (s *SafeLocalBackend) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	path, err := s.scopedDir(req.Path)
	if err != nil {
		return blockedFileInfos(err), nil
	}
	req.Path = path
	return s.Local.LsInfo(ctx, req)
}

// Read reads file content, restricted to SkillsDir.
func (s *SafeLocalBackend) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	path, err := s.scopedFile(req.FilePath)
	if err != nil {
		return &filesystem.FileContent{Content: blockedPrefix + err.Error()}, nil
	}
	req.FilePath = path
	return s.Local.Read(ctx, req)
}

// maxMultiModalBytes bounds one multimodal read.
//
// The bytes are base64-encoded into the model's context, where an oversized file
// is not a slow request but a rejected one. The cap is on the file, not on the
// encoded form, so it is readable as a limit on what a run may look at.
const maxMultiModalBytes = 8 << 20

// MultiModalRead reads an image or PDF for a vision model, restricted to
// SkillsDir.
//
// The wrapped local backend does not implement this, so the read happens here.
// Classification is by extension because that is all a sandbox file has: there
// is no content sniffer in this path, and a file misnamed `.png` is the model's
// problem, not a reason to read bytes it did not ask for.
//
// A file that is neither an image nor a PDF is read as text through Read, which
// is what makes this a drop-in for the enhanced read_file tool rather than a
// replacement for it.
func (s *SafeLocalBackend) MultiModalRead(
	ctx context.Context, req *filesystem.MultiModalReadRequest,
) (*filesystem.MultiFileContent, error) {
	path, err := s.scopedFile(req.FilePath)
	if err != nil {
		return &filesystem.MultiFileContent{
			FileContent: &filesystem.FileContent{Content: blockedPrefix + err.Error()},
		}, nil
	}

	kind, mimeType := multimodalKind(path)
	if kind == "" {
		fc, readErr := s.Read(ctx, &filesystem.ReadRequest{
			FilePath: req.FilePath,
			Offset:   req.Offset,
			Limit:    req.Limit,
		})
		if readErr != nil {
			return nil, readErr
		}
		return &filesystem.MultiFileContent{FileContent: fc}, nil
	}

	info, statErr := os.Stat(path)
	if statErr != nil {
		return nil, statErr
	}
	if info.Size() > maxMultiModalBytes {
		return &filesystem.MultiFileContent{
			FileContent: &filesystem.FileContent{Content: fmt.Sprintf(
				"%s文件过大（%d 字节，上限 %d），请改用 read_file 分页读取或换一个小一些的文件",
				kind, info.Size(), maxMultiModalBytes)},
		}, nil
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, readErr
	}
	return &filesystem.MultiFileContent{
		Parts: []filesystem.FileContentPart{{Type: kind, MIMEType: mimeType, Data: data}},
	}, nil
}

// multimodalKind classifies a path for MultiModalRead. It returns an empty kind
// for anything that should be read as text.
//
// PDF page ranges are not honoured: splitting a PDF needs a parser this service
// does not carry, and handing the model the document is the honest fallback —
// the tool's own schema still accepts the argument, so it is documented here
// rather than rejected at the call.
func multimodalKind(path string) (filesystem.FileContentPartType, string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return filesystem.FileContentPartTypeImage, "image/png"
	case ".jpg", ".jpeg":
		return filesystem.FileContentPartTypeImage, "image/jpeg"
	case ".gif":
		return filesystem.FileContentPartTypeImage, "image/gif"
	case ".webp":
		return filesystem.FileContentPartTypeImage, "image/webp"
	case ".pdf":
		return filesystem.FileContentPartTypePDF, "application/pdf"
	default:
		return "", ""
	}
}

// GrepRaw searches files using regex, restricted to SkillsDir.
func (s *SafeLocalBackend) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	path, err := s.scopedDir(req.Path)
	if err != nil {
		return []filesystem.GrepMatch{{Content: blockedPrefix + err.Error(), Path: "security_block"}}, nil
	}
	req.Path = path
	return s.Local.GrepRaw(ctx, req)
}

// GlobInfo matches files using glob, restricted to SkillsDir.
func (s *SafeLocalBackend) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	path, err := s.scopedDir(req.Path)
	if err != nil {
		return blockedFileInfos(err), nil
	}
	req.Path = path
	return s.Local.GlobInfo(ctx, req)
}

// Write writes files, restricted to SkillsDir.
func (s *SafeLocalBackend) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	path, err := s.scopedFile(req.FilePath)
	if err != nil {
		return err
	}
	req.FilePath = path
	return s.Local.Write(ctx, req)
}

// Edit replaces content inside files, restricted to SkillsDir.
func (s *SafeLocalBackend) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	path, err := s.scopedFile(req.FilePath)
	if err != nil {
		return err
	}
	req.FilePath = path
	return s.Local.Edit(ctx, req)
}

// Command execution:

// guardedCommand validates a raw command string and returns the shell-safe
// command to run. A non-nil response is a rejection the caller returns as-is;
// in that case err is nil (a refusal is a result, not a failure).
//
// The command is rebuilt from the tokenized args rather than forwarded
// verbatim: parseCommandLine treats only whitespace and quotes as separators,
// so shell metacharacters like ';', '|', '&&' or '$(...)' stay glued onto an
// argument token and pass the args[0] whitelist check unchanged — yet the raw
// string, once handed to /bin/sh -c, would let the shell interpret them as
// separators or substitutions. Quoting every token neutralizes that.
func (s *SafeLocalBackend) guardedCommand(input *filesystem.ExecuteRequest) (string, *filesystem.ExecuteResponse) {
	reject := func(message string) (string, *filesystem.ExecuteResponse) {
		exitCode := 127
		return "", &filesystem.ExecuteResponse{Output: blockedPrefix + message, ExitCode: &exitCode}
	}

	if s.SkillsDir == "" {
		return reject("SkillsDir 未配置")
	}
	args, err := parseCommandLine(input.Command)
	if err != nil {
		return reject(fmt.Sprintf("解析命令失败: %v", err))
	}
	if len(args) == 0 {
		return reject("执行命令不能为空")
	}
	if executable := filepath.Base(args[0]); !isAllowedExecutable(executable) {
		return reject(fmt.Sprintf("该命令不在允许执行的白名单中: %s", executable))
	}
	return fmt.Sprintf("cd %s && %s", shellQuote(s.SkillsDir), quoteShellArgs(args)), nil
}

// isAllowedExecutable reports whether name is on the command whitelist.
func isAllowedExecutable(name string) bool {
	return slices.Contains(allowedExecutables, name)
}

// Execute runs a whitelisted command strictly inside the SkillsDir working directory.
func (s *SafeLocalBackend) Execute(ctx context.Context, input *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	command, rejection := s.guardedCommand(input)
	if rejection != nil {
		return rejection, nil
	}
	return s.Local.Execute(ctx, &filesystem.ExecuteRequest{
		Command:            command,
		RunInBackendGround: input.RunInBackendGround,
	})
}

// ExecuteStreaming is the streaming variant of Execute; it shares the same
// guard and returns a single-element stream on rejection.
func (s *SafeLocalBackend) ExecuteStreaming(
	ctx context.Context, input *filesystem.ExecuteRequest,
) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	command, rejection := s.guardedCommand(input)
	if rejection != nil {
		return schema.StreamReaderFromArray([]*filesystem.ExecuteResponse{rejection}), nil
	}
	return s.Local.ExecuteStreaming(ctx, &filesystem.ExecuteRequest{
		Command:            command,
		RunInBackendGround: input.RunInBackendGround,
	})
}

// Shell quoting:

// parseCommandLine splits a command line string into its executable and
// arguments, respecting quotes.
func parseCommandLine(cmd string) ([]string, error) {
	var (
		args     []string
		current  strings.Builder
		inDouble bool
		inSingle bool
		escaped  bool
	)

	for _, r := range strings.TrimSpace(cmd) {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case (r == ' ' || r == '\t') && !inDouble && !inSingle:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if inDouble || inSingle {
		return nil, fmt.Errorf("unclosed quotes in command line")
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args, nil
}

// shellQuote wraps s in single quotes for safe inclusion in a POSIX shell
// command, escaping any embedded single quotes as '\” (close quote, escaped
// literal quote, reopen quote). A single-quoted string has no special
// characters to the shell other than the single quote itself, so this is
// sufficient to neutralize ';', '|', '&&', '$(...)', backticks, etc.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// quoteShellArgs joins args into a single shell-safe command string, with each
// argument individually quoted via shellQuote.
func quoteShellArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}
