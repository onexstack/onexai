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

package command

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/pkg/commandusage"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	customK8sName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	unknownFlagRe = regexp.MustCompile(`unknown flag: --([A-Za-z0-9-]+)`)
)

// customCommands builds the user-defined commands a conversation may run.
//
// They are cobra commands in the ordinary sense: flags are registered from the
// declaration, Args/PreRunE enforce the argument shape and RunE hands the parsed
// values to the runner. A name already taken by a built-in or a skill is dropped
// rather than allowed to shadow it.
func customCommands(
	customs []runtime.CustomCommand, taken map[string]struct{},
	sr *runtime.StaticRuntime, out runtime.Responder,
) []*cobra.Command {
	if len(customs) == 0 {
		return nil
	}

	// Two passes on purpose: every command's own name is claimed before any
	// alias is, so one command's alias can never take another command's name.
	kept := make([]runtime.CustomCommand, 0, len(customs))
	for _, cc := range customs {
		if cc.Name == "" {
			continue
		}
		if _, clash := taken[cc.Name]; clash {
			slog.Warn("a custom command was dropped: its name is already a command",
				"command", cc.Name, "agent", cc.Agent)
			continue
		}
		taken[cc.Name] = struct{}{}
		kept = append(kept, cc)
	}

	cmds := make([]*cobra.Command, 0, len(kept))
	for _, cc := range kept {
		aliases := make([]string, 0, len(cc.Aliases))
		for _, alias := range cc.Aliases {
			if alias == "" || alias == cc.Name {
				continue
			}
			if _, clash := taken[alias]; clash {
				slog.Warn("a custom command alias was dropped: its name is already a command",
					"command", cc.Name, "alias", alias, "agent", cc.Agent)
				continue
			}
			taken[alias] = struct{}{}
			aliases = append(aliases, alias)
		}
		cmds = append(cmds, newCustomCommand(cc, aliases, sr, out))
	}
	return cmds
}

// reserveNames records the names a set of built commands answers to.
func reserveNames(taken map[string]struct{}, cmds []*cobra.Command) {
	for _, cmd := range cmds {
		taken[cmd.Name()] = struct{}{}
		for _, alias := range cmd.Aliases {
			taken[alias] = struct{}{}
		}
	}
}

// newCustomCommand builds one command from its declaration.
func newCustomCommand(
	cc runtime.CustomCommand, aliases []string,
	sr *runtime.StaticRuntime, out runtime.Responder,
) *cobra.Command {
	binder := newParamBinder(cc.Parameters)
	// usage is derived from the declarations, not from the author-editable
	// spec.usage: an error must never teach the user an invocation shape the
	// parser does not accept. spec.usage is still shown as an example.
	usage := customUsage(cc, binder)

	cmd := &cobra.Command{
		Use:           binder.use(cc.Name),
		Aliases:       aliases,
		Short:         customShort(cc),
		Long:          customLong(cc, usage),
		Example:       customExample(cc, usage),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := binder.validateArgs(cmd, args); err != nil {
				return runtime.NewUserError("%v\n用法: %s", err, usage)
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := binder.check(cmd, args); err != nil {
				return runtime.NewUserError("%v\n用法: %s", err, usage)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			params, err := binder.collect(cmd, args)
			if err != nil {
				return runtime.NewUserError("%v\n用法: %s", err, usage)
			}
			if sr == nil || sr.CustomRunner == nil {
				return runtime.NewUserError("自定义命令未启用。")
			}
			return sr.CustomRunner.RunCustom(cmd.Context(), runtime.From(cmd.Context()), out, cc, params)
		},
	}

	binder.register(cmd.Flags())
	// The root silences usage so a failure is reported once; a parameter error
	// is the one place the user needs the usage line, so it is added here.
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		if hint := binder.flagErrorHint(err); hint != "" {
			return runtime.NewUserError("%s\n用法: %s", hint, usage)
		}
		return runtime.NewUserError("%v\n用法: %s", err, usage)
	})
	// Help for a custom command must land in the chat, not on stdout.
	cmd.SetOut(out)
	cmd.SetErr(out)

	// Every custom command is an MCP tool candidate; the bridge's per-caller
	// filter decides who sees it. A prompt command additionally carries the
	// Lark-only marker, which hides it from surfaces that cannot draw a turn —
	// on MCP it returns the rendered prompt text instead of starting one.
	annotations := map[string]string{
		chat.MCPTool:       chat.True,
		chat.CustomCommand: cc.Name,
	}
	if cc.Type == "prompt" {
		annotations[chat.LarkOnly] = chat.True
	}
	cmd.Annotations = annotations
	return cmd
}

func customUsage(cc runtime.CustomCommand, binder *paramBinder) string {
	return commandusage.Line(cc.Name, binder.usageParameters())
}

// customExample keeps the author's display hint for help output. Errors and the
// Long "用法:" line use the canonical line instead.
func customExample(cc runtime.CustomCommand, canonical string) string {
	if strings.TrimSpace(cc.Usage) == "" {
		return "  " + canonical
	}
	return "  /" + cc.Name + " " + strings.TrimSpace(cc.Usage)
}

func customShort(cc runtime.CustomCommand) string {
	if strings.TrimSpace(cc.DisplayName) != "" {
		return cc.DisplayName
	}
	if strings.TrimSpace(cc.Description) != "" {
		return cc.Description
	}
	return "自定义命令"
}

func customLong(cc runtime.CustomCommand, usage string) string {
	var b strings.Builder
	if strings.TrimSpace(cc.Description) != "" {
		b.WriteString(cc.Description)
		b.WriteString("\n\n")
	}
	b.WriteString("用法: ")
	b.WriteString(usage)
	if cc.Type != "" {
		fmt.Fprintf(&b, "\n类型: %s", cc.Type)
	}
	return b.String()
}

// ──────────────────────────────────────────────────────────────────────────────
// Parameter binding
// ──────────────────────────────────────────────────────────────────────────────

// paramValue holds one flag's value. Exactly one pointer is set, chosen by the
// declared type.
type paramValue struct {
	kind     string
	str      *string
	boolean  *bool
	integer  *int64
	number   *float64
	duration *time.Duration
	array    *[]string
}

// paramBinder turns a declaration into pflag flags and back into a parameter
// map. It is deliberately the same shape as the built-in commands' use of
// cobra: flags are real pflag values, so help, shorthand and parse errors are
// the platform's own.
type paramBinder struct {
	params     []runtime.CustomCommandParameter
	positional []int
	values     map[string]*paramValue
}

func newParamBinder(params []runtime.CustomCommandParameter) *paramBinder {
	b := &paramBinder{
		params: params,
		values: make(map[string]*paramValue, len(params)),
	}
	for i := range params {
		p := &params[i]
		if p.In == "positional" {
			b.positional = append(b.positional, i)
		}
		b.values[p.Name] = newParamValue(p)
	}
	return b
}

func newParamValue(p *runtime.CustomCommandParameter) *paramValue {
	kind := p.Type
	if kind == "" {
		kind = "string"
	}
	v := &paramValue{kind: kind}
	switch kind {
	case "integer":
		n := int64(0)
		if p.DefaultJSON != "" {
			_ = json.Unmarshal([]byte(p.DefaultJSON), &n)
		}
		v.integer = &n
	case "number":
		n := float64(0)
		if p.DefaultJSON != "" {
			_ = json.Unmarshal([]byte(p.DefaultJSON), &n)
		}
		v.number = &n
	case "boolean":
		b := false
		if p.DefaultJSON != "" {
			_ = json.Unmarshal([]byte(p.DefaultJSON), &b)
		}
		v.boolean = &b
	case "duration":
		d := time.Duration(0)
		if p.DefaultJSON != "" {
			var s string
			if json.Unmarshal([]byte(p.DefaultJSON), &s) == nil {
				d, _ = time.ParseDuration(s)
			}
		}
		v.duration = &d
	case "array":
		a := []string(nil)
		if p.DefaultJSON != "" {
			if err := json.Unmarshal([]byte(p.DefaultJSON), &a); err != nil {
				// Elements that are not strings (numbers, objects) cannot be
				// spread into a string slice. Keep the document instead of an
				// empty default: collect() parses it by the declared type.
				a = []string{p.DefaultJSON}
			}
		}
		v.array = &a
	default:
		s := ""
		if p.DefaultJSON != "" {
			if err := json.Unmarshal([]byte(p.DefaultJSON), &s); err != nil {
				// An object or json parameter's default is a JSON document, not
				// a string. Keep the text: the flag still shows it, and collect()
				// parses it by the declared type.
				s = p.DefaultJSON
			}
		}
		v.str = &s
	}
	return v
}

// usageParameters projects the declaration onto the shared usage renderer.
func (b *paramBinder) usageParameters() []commandusage.Parameter {
	out := make([]commandusage.Parameter, 0, len(b.params))
	for i := range b.params {
		p := &b.params[i]
		if p.Name == "" {
			continue
		}
		out = append(out, commandusage.Parameter{
			Name:     p.Name,
			In:       p.In,
			Type:     p.Type,
			Required: p.Required,
		})
	}
	return out
}

// flagErrorHint explains the common mistaken invocation: a parameter declared
// positionally cannot be supplied as --name, and pflag's "unknown flag" does
// not say why.
func (b *paramBinder) flagErrorHint(err error) string {
	if err == nil {
		return ""
	}
	m := unknownFlagRe.FindStringSubmatch(err.Error())
	if m == nil {
		return ""
	}
	name := m[1]
	for i := range b.params {
		p := &b.params[i]
		if p.In == "positional" && p.Name == name {
			return fmt.Sprintf("参数 %q 是位置参数，请直接写值，不要使用 --%s。", p.Name, p.Name)
		}
	}
	return ""
}

// register adds the declared flags. A duplicate or unusable name is skipped
// rather than panicking: the declaration is validated at write time, but an old
// or hand-written object must not take the whole command tree down.
func (b *paramBinder) register(fs *pflag.FlagSet) {
	for i := range b.params {
		p := &b.params[i]
		if p.In == "positional" {
			continue
		}
		if fs.Lookup(p.Name) != nil {
			slog.Warn("custom command parameter was skipped: duplicate flag name", "parameter", p.Name)
			continue
		}
		short := ""
		if len(p.Short) == 1 && fs.ShorthandLookup(p.Short) == nil {
			short = p.Short
		}
		v := b.values[p.Name]
		switch v.kind {
		case "integer":
			fs.Int64VarP(v.integer, p.Name, short, *v.integer, p.Description)
		case "number":
			fs.Float64VarP(v.number, p.Name, short, *v.number, p.Description)
		case "boolean":
			fs.BoolVarP(v.boolean, p.Name, short, *v.boolean, p.Description)
		case "duration":
			fs.DurationVarP(v.duration, p.Name, short, *v.duration, p.Description)
		case "array":
			fs.StringSliceVarP(v.array, p.Name, short, *v.array, p.Description)
		default:
			fs.StringVarP(v.str, p.Name, short, *v.str, p.Description)
		}
	}
}

// use returns the cobra Use line, with positional arguments spelled out.
func (b *paramBinder) use(name string) string {
	parts := []string{name}
	for _, idx := range b.positional {
		p := &b.params[idx]
		if p.Required {
			parts = append(parts, "<"+p.Name+">")
		} else {
			parts = append(parts, "["+p.Name+"]")
		}
	}
	return strings.Join(parts, " ")
}

// validateArgs enforces the positional count.
func (b *paramBinder) validateArgs(_ *cobra.Command, args []string) error {
	required := 0
	for _, idx := range b.positional {
		if b.params[idx].Required {
			required++
		}
	}
	if len(args) < required {
		return runtime.NewUserError("缺少位置参数（需要 %d 个，给了 %d 个）", required, len(args))
	}
	if len(args) > len(b.positional) {
		return runtime.NewUserError("位置参数过多（最多 %d 个，给了 %d 个）", len(b.positional), len(args))
	}
	return nil
}

// check validates what pflag cannot: required flags, enums, formats and JSON.
func (b *paramBinder) check(cmd *cobra.Command, args []string) error {
	for i := range b.params {
		p := &b.params[i]
		v := b.values[p.Name]

		provided := false
		if p.In == "positional" {
			index := b.positionalIndex(i)
			provided = index >= 0 && index < len(args)
			if !provided && p.Required {
				return runtime.NewUserError("缺少位置参数 %q", p.Name)
			}
			if !provided {
				continue
			}
			if err := b.validateText(p, args[index]); err != nil {
				return err
			}
			continue
		}

		provided = cmd.Flags().Changed(p.Name)
		if !provided {
			if p.Required {
				return runtime.NewUserError("缺少必填参数 --%s", p.Name)
			}
			continue
		}
		if err := b.validateValue(p, v); err != nil {
			return err
		}
	}
	return nil
}

func (b *paramBinder) positionalIndex(index int) int {
	for i, idx := range b.positional {
		if idx == index {
			return i
		}
	}
	return -1
}

func (b *paramBinder) validateValue(p *runtime.CustomCommandParameter, v *paramValue) error {
	text := ""
	switch v.kind {
	case "integer":
		text = strconv.FormatInt(*v.integer, 10)
	case "number":
		text = strconv.FormatFloat(*v.number, 'f', -1, 64)
	case "boolean":
		text = strconv.FormatBool(*v.boolean)
	case "duration":
		text = v.duration.String()
	case "array":
		return nil
	default:
		text = *v.str
	}
	return b.validateText(p, text)
}

func (b *paramBinder) validateText(p *runtime.CustomCommandParameter, text string) error {
	if len(p.Enum) > 0 {
		matched := false
		for _, allowed := range p.Enum {
			if text == allowed {
				matched = true
				break
			}
		}
		if !matched {
			return runtime.NewUserError("参数 %q 必须是 [%s] 之一", p.Name, strings.Join(p.Enum, ", "))
		}
	}
	switch p.Format {
	case "k8s-name":
		if !customK8sName.MatchString(text) {
			return runtime.NewUserError("参数 %q 必须是合法的 Kubernetes 资源名", p.Name)
		}
	case "url":
		u, err := url.Parse(text)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return runtime.NewUserError("参数 %q 必须是 http(s) URL", p.Name)
		}
	case "json":
		if !json.Valid([]byte(text)) {
			return runtime.NewUserError("参数 %q 必须是合法 JSON", p.Name)
		}
	case "duration":
		if _, err := time.ParseDuration(text); err != nil {
			return runtime.NewUserError("参数 %q 必须是时长（如 30s、2m）", p.Name)
		}
	}
	if p.Type == "object" || p.Type == "json" {
		if !json.Valid([]byte(text)) {
			return runtime.NewUserError("参数 %q 必须是合法 JSON", p.Name)
		}
	}
	return nil
}

// collect returns the parameter map sent to the agent service. Only values the
// user actually supplied are included; the server applies the declared defaults,
// so a default lives in one place.
func (b *paramBinder) collect(cmd *cobra.Command, args []string) (map[string]any, error) {
	out := make(map[string]any, len(b.params))
	for i := range b.params {
		p := &b.params[i]
		v := b.values[p.Name]

		if p.In == "positional" {
			index := b.positionalIndex(i)
			if index < 0 || index >= len(args) {
				continue
			}
			value, err := parseParamText(v.kind, args[index])
			if err != nil {
				return nil, err
			}
			out[p.Name] = value
			continue
		}

		if !cmd.Flags().Changed(p.Name) {
			continue
		}
		switch v.kind {
		case "integer":
			out[p.Name] = *v.integer
		case "number":
			out[p.Name] = *v.number
		case "boolean":
			out[p.Name] = *v.boolean
		case "duration":
			out[p.Name] = v.duration.String()
		case "array":
			values := make([]any, 0, len(*v.array))
			for _, item := range *v.array {
				values = append(values, item)
			}
			out[p.Name] = values
		default:
			value, err := parseParamText(v.kind, *v.str)
			if err != nil {
				return nil, err
			}
			out[p.Name] = value
		}
	}
	return out, nil
}

func parseParamText(kind, text string) (any, error) {
	switch kind {
	case "integer":
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, runtime.NewUserError("%q 不是整数", text)
		}
		return n, nil
	case "number":
		n, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, runtime.NewUserError("%q 不是数字", text)
		}
		return n, nil
	case "boolean":
		b, err := strconv.ParseBool(text)
		if err != nil {
			return nil, runtime.NewUserError("%q 不是布尔值", text)
		}
		return b, nil
	case "array":
		parts := strings.Split(text, ",")
		values := make([]any, 0, len(parts))
		for _, part := range parts {
			values = append(values, strings.TrimSpace(part))
		}
		return values, nil
	case "object", "json":
		var value any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			return nil, runtime.NewUserError("参数值不是合法 JSON: %v", err)
		}
		return value, nil
	default:
		return text, nil
	}
}
