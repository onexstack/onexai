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

package customcmd

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
)

const (
	// TemplateLimit bounds one rendered template. A command body is copied into
	// logs and chat messages, so an unbounded render is an unbounded bill.
	TemplateLimit = 64 * 1024
	// ResultLimit bounds the text a command returns to the chat surface.
	ResultLimit = 16 * 1024
)

// TemplateData is what a command template sees. It is deliberately a small,
// read-only view: templates are authored by whoever writes the resource, and
// they must not reach the process.
type TemplateData struct {
	Params       map[string]any
	Command      string
	Agent        string
	Conversation map[string]string
	User         *authenticationv1.UserInfo
	DryRun       bool
	Now          time.Time
}

// RenderTemplate renders one command template with missing keys treated as an
// error. A silent empty value is the one failure a command author cannot debug.
func RenderTemplate(name, text string, data TemplateData) (string, error) {
	return RenderWith(name, text, data)
}

// RenderWith is RenderTemplate for a data shape other than TemplateData; the
// output renderer needs the response itself.
func RenderWith(name, text string, data any) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("parsing template %q: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", friendlyRenderError(name, err, data)
	}
	if buf.Len() > TemplateLimit {
		return "", fmt.Errorf("rendering template %q: output exceeds %d bytes", name, TemplateLimit)
	}
	return buf.String(), nil
}

var (
	fieldNotFoundRe = regexp.MustCompile(`can't evaluate field ([A-Za-z0-9_]+) in type`)
	missingKeyRe    = regexp.MustCompile(`map has no entry for key "([^"]+)"`)
)

// friendlyRenderError turns the two deterministic template mistakes into a
// message the command author can act on. Every other execution error keeps its
// original context: a nil pointer or a bad function argument is not a field
// typo, and guessing would send the author in the wrong direction.
func friendlyRenderError(name string, err error, data any) error {
	msg := err.Error()
	fields, params := templateDataFields(data)

	if m := fieldNotFoundRe.FindStringSubmatch(msg); m != nil {
		field := m[1]
		if strings.Contains(msg, "type v1.UserInfo") {
			return fmt.Errorf("模板字段 .User.%s 不存在；.User 可用字段：Username、UID、Groups、Extra", field)
		}
		if field == "Param" {
			return fmt.Errorf(
				"模板字段 .Param 不存在；参数集合是 .Params（复数），例如 {{.Params.name}}。%s",
				fieldsHint(fields, params))
		}
		return fmt.Errorf("模板字段 .%s 不存在。%s", field, fieldsHint(fields, params))
	}
	if m := missingKeyRe.FindStringSubmatch(msg); m != nil {
		return fmt.Errorf("模板引用了未声明的参数 %q。%s", m[1], fieldsHint(fields, params))
	}
	return fmt.Errorf("rendering template %q: %w", name, err)
}

// templateDataFields returns the root fields and declared parameter names the
// runtime is rendering against. Params is populated by ValidateParams, so the
// keys are exactly the declared parameters (including zero values).
func templateDataFields(data any) ([]string, []string) {
	switch d := data.(type) {
	case TemplateData:
		return []string{"Params", "Command", "Agent", "Conversation", "User", "DryRun", "Now"},
			sortedMapKeys(d.Params)
	case *TemplateData:
		if d == nil {
			return nil, nil
		}
		return []string{"Params", "Command", "Agent", "Conversation", "User", "DryRun", "Now"},
			sortedMapKeys(d.Params)
	case OutputTemplateData:
		return []string{"Status", "Header", "Body", "JSON", "Params"}, sortedMapKeys(d.Params)
	case *OutputTemplateData:
		if d == nil {
			return nil, nil
		}
		return []string{"Status", "Header", "Body", "JSON", "Params"}, sortedMapKeys(d.Params)
	default:
		return nil, nil
	}
}

func sortedMapKeys(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fieldsHint(fields, params []string) string {
	var b strings.Builder
	if len(fields) > 0 {
		b.WriteString("可用字段：")
		b.WriteString(strings.Join(fields, "、"))
	}
	if len(params) > 0 {
		if b.Len() > 0 {
			b.WriteString("；")
		}
		b.WriteString("已声明参数：")
		b.WriteString(strings.Join(params, "、"))
	}
	return b.String()
}
