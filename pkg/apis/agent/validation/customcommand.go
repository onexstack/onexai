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

package validation

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"text/template"
	"time"

	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// Custom command vocabulary. The chat service builds a cobra command from these
// values, so a value it cannot express is rejected at write time rather than
// discovered when someone types the command.
var (
	KnownCustomCommandTypes    = []string{"prompt", "http", "tool"}
	KnownCommandParameterIns   = []string{"flag", "positional"}
	KnownCommandParameterTypes = []string{
		"string", "integer", "number", "boolean", "duration", "array", "object", "json",
	}
	KnownCommandFormats    = []string{"k8s-name", "url", "json", "duration"}
	KnownHTTPMethods       = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}
	KnownSideEffectClasses = []string{"None", "NoneOnDryRun", "Some", "Unknown"}
	KnownOutputFormats     = []string{"text", "json", "template"}
)

const (
	commandNameMaxLength = 32
	commandTemplateLimit = 64 * 1024
	commandParameterMax  = 32
	commandAliasMax      = 8
)

var (
	commandNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	commandParamPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	commandShortPattern = regexp.MustCompile(`^[A-Za-z]$`)
)

// ValidateCustomCommandName checks the metadata.name, which is the slash name.
func ValidateCustomCommandName(name string, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if !commandNamePattern.MatchString(name) {
		allErrs = append(allErrs, field.Invalid(fldPath, name,
			"must be lowercase letters, digits and single hyphens, starting with a letter"))
	}
	if len(name) > commandNameMaxLength {
		allErrs = append(allErrs, field.Invalid(fldPath, name,
			fmt.Sprintf("must be at most %d characters", commandNameMaxLength)))
	}
	return allErrs
}

// ValidateCustomCommand checks a custom command definition.
func ValidateCustomCommand(spec *agent.CustomCommandSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if spec.Type == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("type"), "a command must name its type"))
	} else if !contains(KnownCustomCommandTypes, spec.Type) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("type"), spec.Type, KnownCustomCommandTypes))
	}

	allErrs = append(allErrs, validateCommandAliases(spec.Aliases, fldPath.Child("aliases"))...)
	allErrs = append(allErrs, validateCommandParameters(spec.Parameters, fldPath.Child("parameters"))...)
	allErrs = append(allErrs, validateCommandBody(spec, fldPath)...)

	if spec.TimeoutSeconds < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("timeoutSeconds"), spec.TimeoutSeconds,
			"must not be negative"))
	}
	return allErrs
}

// validateCommandBody checks the one-of between prompt, http and tool.
func validateCommandBody(spec *agent.CustomCommandSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	set := 0
	if spec.Prompt != nil {
		set++
	}
	if spec.HTTP != nil {
		set++
	}
	if spec.Tool != nil {
		set++
	}
	if set != 1 {
		return append(allErrs, field.Invalid(fldPath, spec.Type,
			"exactly one of prompt, http and tool must be set"))
	}

	commandScope := commandTemplateScope(spec.Parameters)
	outputScope := outputTemplateScope(spec.Parameters)

	switch spec.Type {
	case "prompt":
		if spec.Prompt == nil {
			return append(allErrs, field.Required(fldPath.Child("prompt"), "type prompt requires a prompt body"))
		}
		if spec.Output != nil {
			return append(allErrs, field.Forbidden(fldPath.Child("output"),
				"a prompt command is handed to the agent verbatim; output does not apply"))
		}
		if strings.TrimSpace(spec.Prompt.Template) == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("prompt", "template"),
				"a prompt command must have a template"))
		}
		allErrs = append(allErrs, validateCommandTemplate(spec.Prompt.Template, commandScope,
			fldPath.Child("prompt", "template"))...)
	case "http":
		if spec.HTTP == nil {
			return append(allErrs, field.Required(fldPath.Child("http"), "type http requires an http body"))
		}
		allErrs = append(allErrs, validateHTTPCommand(spec.HTTP, commandScope, fldPath.Child("http"))...)
	case "tool":
		if spec.Tool == nil {
			return append(allErrs, field.Required(fldPath.Child("tool"), "type tool requires a tool body"))
		}
		allErrs = append(allErrs, validateToolCommand(spec.Tool, commandScope, fldPath.Child("tool"))...)
	}

	if spec.Output != nil && spec.Type != "prompt" {
		allErrs = append(allErrs, validateCommandOutput(spec.Output, outputScope, fldPath.Child("output"))...)
	}
	return allErrs
}

func validateCommandAliases(aliases []string, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if len(aliases) > commandAliasMax {
		allErrs = append(allErrs, field.TooMany(fldPath, len(aliases), commandAliasMax))
	}
	seen := make(map[string]struct{}, len(aliases))
	for i, alias := range aliases {
		path := fldPath.Index(i)
		allErrs = append(allErrs, ValidateCustomCommandName(alias, path)...)
		if _, dup := seen[alias]; dup {
			allErrs = append(allErrs, field.Duplicate(path, alias))
		}
		seen[alias] = struct{}{}
	}
	return allErrs
}

func validateCommandParameters(params []agent.CommandParameter, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if len(params) > commandParameterMax {
		allErrs = append(allErrs, field.TooMany(fldPath, len(params), commandParameterMax))
	}

	names := make(map[string]struct{}, len(params))
	shorts := make(map[string]struct{}, len(params))
	positional := 0
	for i := range params {
		p := &params[i]
		path := fldPath.Index(i)

		if !commandParamPattern.MatchString(p.Name) {
			allErrs = append(allErrs, field.Invalid(path.Child("name"), p.Name,
				"must be lowercase letters, digits and hyphens, starting with a letter"))
		}
		if _, dup := names[p.Name]; dup {
			allErrs = append(allErrs, field.Duplicate(path.Child("name"), p.Name))
		}
		names[p.Name] = struct{}{}

		if p.In != "" && !contains(KnownCommandParameterIns, p.In) {
			allErrs = append(allErrs, field.NotSupported(path.Child("in"), p.In, KnownCommandParameterIns))
		}
		if p.In == "positional" {
			positional++
			if p.Short != "" {
				allErrs = append(allErrs, field.Invalid(path.Child("short"),
					p.Short, "a positional parameter has no flag shorthand"))
			}
		}
		if p.Short != "" {
			if !commandShortPattern.MatchString(p.Short) {
				allErrs = append(allErrs, field.Invalid(path.Child("short"), p.Short,
					"must be a single letter"))
			}
			if _, dup := shorts[p.Short]; dup {
				allErrs = append(allErrs, field.Duplicate(path.Child("short"), p.Short))
			}
			shorts[p.Short] = struct{}{}
		}

		ptype := p.Type
		if ptype == "" {
			ptype = "string"
		}
		if !contains(KnownCommandParameterTypes, ptype) {
			allErrs = append(allErrs, field.NotSupported(path.Child("type"), p.Type, KnownCommandParameterTypes))
		}

		if p.Required && p.Default != nil {
			allErrs = append(allErrs, field.Invalid(path, p.Name,
				"required and default are mutually exclusive"))
		}
		if p.Default != nil {
			allErrs = append(allErrs, validateParameterDefault(ptype, p.Default.Raw, path.Child("default"))...)
		}
		if len(p.Enum) > 0 {
			if !isScalarParameter(ptype) {
				allErrs = append(allErrs, field.Invalid(path.Child("enum"), ptype,
					"enum applies to scalar parameters only"))
			}
			for j, v := range p.Enum {
				if v == "" {
					allErrs = append(allErrs, field.Invalid(path.Child("enum").Index(j), v,
						"must not be empty"))
				}
			}
		}
		if p.Format != "" {
			if !contains(KnownCommandFormats, p.Format) {
				allErrs = append(allErrs, field.NotSupported(path.Child("format"), p.Format, KnownCommandFormats))
			}
			if ptype != "string" && ptype != "duration" {
				allErrs = append(allErrs, field.Invalid(path.Child("format"), p.Format,
					"format applies to string and duration parameters only"))
			}
		}
		if p.Schema != nil {
			if !isStructuredParameter(ptype) {
				allErrs = append(allErrs, field.Invalid(path.Child("schema"), ptype,
					"schema applies to object, array and json parameters only"))
			}
			allErrs = append(allErrs, validateJSONObject(p.Schema.Raw, path.Child("schema"))...)
		}
	}

	// Positional parameters are consumed in declaration order, so a required one
	// after an optional one can never be told apart from the optional one.
	if positional > 0 {
		seenOptional := false
		for i := range params {
			if params[i].In != "positional" {
				continue
			}
			if params[i].Required && seenOptional {
				allErrs = append(allErrs, field.Invalid(fldPath.Index(i), params[i].Name,
					"a required positional parameter must precede optional ones"))
			}
			if !params[i].Required {
				seenOptional = true
			}
		}
	}
	return allErrs
}

func validateHTTPCommand(
	spec *agent.HTTPCommandSpec, scope templateScope, fldPath *field.Path,
) field.ErrorList {
	var allErrs field.ErrorList

	method := spec.Method
	if method == "" {
		method = "GET"
	}
	if !contains(KnownHTTPMethods, method) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("method"), spec.Method, KnownHTTPMethods))
	}
	if (method == "GET" || method == "HEAD") && spec.Body != "" {
		allErrs = append(allErrs, field.Forbidden(fldPath.Child("body"),
			"a GET or HEAD request must not carry a body"))
	}

	allErrs = append(allErrs, validateHTTPTarget(spec, scope, fldPath)...)

	if spec.SideEffects != "" && !contains(KnownSideEffectClasses, spec.SideEffects) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("sideEffects"), spec.SideEffects,
			KnownSideEffectClasses))
	}
	if spec.SecretRef != nil && spec.SecretRef.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("secretRef", "name"),
			"secretRef must name a Secret"))
	}
	allErrs = append(allErrs, validateHTTPWire(spec, scope, fldPath)...)
	return allErrs
}

// validateHTTPTarget checks the mutually exclusive url/service pair, or the
// shared ClientConfig when it is set.
func validateHTTPTarget(
	spec *agent.HTTPCommandSpec, scope templateScope, fldPath *field.Path,
) field.ErrorList {
	if spec.ClientConfig != nil {
		allErrs := validateClientConfig(spec.ClientConfig, fldPath.Child("clientConfig"))
		if spec.ClientConfig.URL != nil {
			// The scheme and host of a custom command must stay literal: a
			// templated host would let a parameter choose the destination.
			allErrs = append(allErrs, validateCommandURL(*spec.ClientConfig.URL, fldPath.Child("clientConfig", "url"))...)
			allErrs = append(allErrs, validateCommandTemplate(*spec.ClientConfig.URL, scope, fldPath.Child("clientConfig", "url"))...)
		}
		return allErrs
	}

	var allErrs field.ErrorList
	switch {
	case spec.URL == "" && spec.Service == nil:
		allErrs = append(allErrs, field.Required(fldPath, "one of url and service must be set"))
	case spec.URL != "" && spec.Service != nil:
		allErrs = append(allErrs, field.Invalid(fldPath, "", "url and service are mutually exclusive"))
	case spec.URL != "":
		allErrs = append(allErrs, validateCommandURL(spec.URL, fldPath.Child("url"))...)
		allErrs = append(allErrs, validateCommandTemplate(spec.URL, scope, fldPath.Child("url"))...)
	case spec.Service != nil:
		allErrs = append(allErrs, validateServiceReference(spec.Service, fldPath.Child("service"))...)
	}
	return allErrs
}

// validateServiceReference checks that a service target names an in-cluster
// Service.
//
// Both parts must be DNS labels. Without that rule a value such as
// "169.254.169.254" renders as a link-local address instead of a Service name,
// and a dotted value leaves the cluster's DNS namespace entirely — either one
// turns an http command into a request to wherever the author points it.
func validateServiceReference(spec *agent.ServiceReference, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if spec.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("name"),
			"a service reference must name a Service"))
	} else if problems := utilvalidation.IsDNS1123Label(spec.Name); len(problems) > 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("name"), spec.Name,
			"must be a DNS label: "+strings.Join(problems, "; ")))
	}
	if spec.Namespace != "" {
		if problems := utilvalidation.IsDNS1123Label(spec.Namespace); len(problems) > 0 {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("namespace"), spec.Namespace,
				"must be a DNS label: "+strings.Join(problems, "; ")))
		}
	}
	if spec.Port < 0 || spec.Port > 65535 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("service", "port"),
			spec.Port, "must be a valid port"))
	}
	return allErrs
}

// validateHTTPWire checks the headers, query and body that will be rendered.
func validateHTTPWire(
	spec *agent.HTTPCommandSpec, scope templateScope, fldPath *field.Path,
) field.ErrorList {
	var allErrs field.ErrorList
	for i, h := range spec.Headers {
		path := fldPath.Child("headers").Index(i)
		if strings.TrimSpace(h.Name) == "" {
			allErrs = append(allErrs, field.Required(path.Child("name"), "a header must have a name"))
		} else if isHopByHopHeader(h.Name) {
			allErrs = append(allErrs, field.Forbidden(path.Child("name"),
				"hop-by-hop headers are managed by the transport"))
		}
		allErrs = append(allErrs, validateCommandTemplate(h.Value, scope, path.Child("value"))...)
	}
	for k, v := range spec.Query {
		allErrs = append(allErrs, validateCommandTemplate(v, scope, fldPath.Child("query").Key(k))...)
	}
	allErrs = append(allErrs, validateCommandTemplate(spec.Body, scope, fldPath.Child("body"))...)
	return allErrs
}

func validateToolCommand(
	spec *agent.ToolCommandSpec, scope templateScope, fldPath *field.Path,
) field.ErrorList {
	var allErrs field.ErrorList
	if spec.MCPServer.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("mcpServer", "name"),
			"a tool command must name its MCP server"))
	}
	if strings.TrimSpace(spec.Tool) == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("tool"), "a tool command must name its tool"))
	}
	if len(spec.Tool) > 256 {
		allErrs = append(allErrs, field.TooLong(fldPath.Child("tool"), "", 256))
	}
	allErrs = append(allErrs, validateCommandTemplate(spec.ArgumentsTemplate, scope,
		fldPath.Child("argumentsTemplate"))...)
	return allErrs
}

func validateCommandOutput(
	spec *agent.CommandOutputSpec, scope templateScope, fldPath *field.Path,
) field.ErrorList {
	var allErrs field.ErrorList
	if spec.Format != "" && !contains(KnownOutputFormats, spec.Format) {
		allErrs = append(allErrs, field.NotSupported(fldPath.Child("format"), spec.Format, KnownOutputFormats))
	}
	if spec.Format == "template" && strings.TrimSpace(spec.Template) == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("template"),
			"output format template requires a template"))
	}
	allErrs = append(allErrs, validateCommandTemplate(spec.Template, scope, fldPath.Child("template"))...)
	if spec.Schema != nil {
		allErrs = append(allErrs, validateJSONObject(spec.Schema.Raw, fldPath.Child("schema"))...)
	}
	return allErrs
}

// validateCommandTemplate compiles one template, bounds its size and checks
// that every statically resolvable field reference exists in the scope.
func validateCommandTemplate(text string, scope templateScope, fldPath *field.Path) field.ErrorList {
	if text == "" {
		return nil
	}
	if len(text) > commandTemplateLimit {
		return field.ErrorList{field.TooLong(fldPath, "", commandTemplateLimit)}
	}
	tmpl, err := template.New("command").Option("missingkey=error").Parse(text)
	if err != nil {
		return field.ErrorList{field.Invalid(fldPath, "", err.Error())}
	}
	var allErrs field.ErrorList
	for _, msg := range validateTemplateFields(tmpl, scope) {
		allErrs = append(allErrs, field.Invalid(fldPath, "", msg))
	}
	return allErrs
}

// validateCommandURL checks that the scheme and host are literal. A templated
// host would let a parameter choose the destination and defeat the allowlist.
func validateCommandURL(raw string, fldPath *field.Path) field.ErrorList {
	allErrs := validateURL(raw, fldPath)
	if len(allErrs) > 0 {
		return allErrs
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return field.ErrorList{field.Invalid(fldPath, raw, "must begin with http:// or https://")}
	}
	rest := raw[strings.Index(raw, "://")+3:]
	host := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host = rest[:i]
	}
	if strings.Contains(host, "{{") {
		return field.ErrorList{field.Invalid(fldPath, raw,
			"the scheme and host must be literal; templates may only appear in the path, query and body")}
	}
	if _, err := url.Parse(raw); err != nil {
		return field.ErrorList{field.Invalid(fldPath, raw, err.Error())}
	}
	return nil
}

func validateJSONObject(raw []byte, fldPath *field.Path) field.ErrorList {
	if len(raw) == 0 {
		return field.ErrorList{field.Required(fldPath, "must be a JSON object")}
	}
	if len(raw) > commandTemplateLimit {
		return field.ErrorList{field.TooLong(fldPath, "", commandTemplateLimit)}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return field.ErrorList{field.Invalid(fldPath, "", err.Error())}
	}
	if _, ok := v.(map[string]any); !ok {
		return field.ErrorList{field.Invalid(fldPath, "", "must be a JSON object")}
	}
	return nil
}

func validateParameterDefault(ptype string, raw []byte, fldPath *field.Path) field.ErrorList {
	if len(raw) == 0 {
		return field.ErrorList{field.Required(fldPath, "a default must carry a JSON value")}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return field.ErrorList{field.Invalid(fldPath, "", err.Error())}
	}
	ok := true
	switch ptype {
	case "string":
		_, ok = v.(string)
	case "integer":
		f, isNum := v.(float64)
		ok = isNum && f == float64(int64(f))
	case "number":
		_, ok = v.(float64)
	case "boolean":
		_, ok = v.(bool)
	case "duration":
		s, isStr := v.(string)
		if isStr {
			_, err := time.ParseDuration(s)
			ok = err == nil
		} else {
			ok = false
		}
	case "array":
		_, ok = v.([]any)
	case "object":
		_, ok = v.(map[string]any)
	case "json":
		ok = true
	}
	if !ok {
		return field.ErrorList{field.Invalid(fldPath, string(raw),
			"default does not match the declared type "+ptype)}
	}
	return nil
}

func isScalarParameter(ptype string) bool {
	switch ptype {
	case "string", "integer", "number", "boolean", "duration":
		return true
	default:
		return false
	}
}

func isStructuredParameter(ptype string) bool {
	switch ptype {
	case "array", "object", "json":
		return true
	default:
		return false
	}
}

func isHopByHopHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "host", "content-length", "transfer-encoding", "connection", "keep-alive",
		"proxy-authenticate", "proxy-authorization", "te", "trailer", "upgrade":
		return true
	default:
		return false
	}
}
