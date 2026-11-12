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
	"encoding/json"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	"github.com/onexstack/onexai/internal/pkg/commandusage"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// ProtoToModel turns the business API's command shape into the row shape.
func ProtoToModel(p *v1.CustomCommand) (*model.CustomCommandM, error) {
	if p == nil {
		return &model.CustomCommandM{}, nil
	}
	row := &model.CustomCommandM{
		Name:        p.GetName(),
		DisplayName: p.GetDisplayName(),
		Description: p.GetDescription(),
		Status:      protoStatus(p.GetEnabled()),
		Labels:      model.StringMap(p.GetLabels()),
		Namespace:   p.GetNamespace(),
	}
	spec := &model.CustomCommandSpecM{
		Type:           p.GetType(),
		Aliases:        p.GetAliases(),
		Usage:          p.GetUsage(),
		Prompt:         promptFromProto(p.GetPrompt()),
		HTTP:           httpFromProto(p.GetHttp()),
		Tool:           toolFromProto(p.GetTool()),
		Output:         outputFromProto(p.GetOutput()),
		TimeoutSeconds: p.GetTimeoutSeconds(),
	}
	for _, param := range p.GetParameters() {
		spec.Parameters = append(spec.Parameters, ParameterFromProto(param))
	}
	if err := row.SetSpec(spec); err != nil {
		return nil, err
	}
	return row, nil
}

// ModelToProto turns the row shape back into the business API's shape.
func ModelToProto(row *model.CustomCommandM) (*v1.CustomCommand, error) {
	spec, err := row.Spec()
	if err != nil {
		return nil, err
	}
	out := &v1.CustomCommand{
		Name:           row.Name,
		DisplayName:    row.DisplayName,
		Status:         conversion.StatusToV1(row.Status),
		Description:    row.Description,
		Enabled:        row.Enabled(),
		Type:           spec.Type,
		Aliases:        spec.Aliases,
		Usage:          spec.Usage,
		Prompt:         promptToProto(spec.Prompt),
		Http:           httpToProto(spec.HTTP),
		Tool:           toolToProto(spec.Tool),
		Output:         outputToProto(spec.Output),
		TimeoutSeconds: spec.TimeoutSeconds,
		Labels:         map[string]string(row.Labels),
		Namespace:      row.Namespace,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	for _, param := range spec.Parameters {
		out.Parameters = append(out.Parameters, ParameterToProto(param))
	}
	return out, nil
}

// ContextToProto narrows a command to what a chat surface needs to build the
// cobra command and its help.
func ContextToProto(row *model.CustomCommandM) (*v1.ContextCustomCommand, error) {
	spec, err := row.Spec()
	if err != nil {
		return nil, err
	}
	usage := strings.TrimSpace(spec.Usage)
	if usage == "" {
		// spec.usage stores only the part after the command name (the console
		// renders the name in its own column). Without it, show the shape
		// derived from the declarations so /help and the console always have
		// something truthful to display.
		usage = CanonicalArguments(spec.Parameters)
	}
	out := &v1.ContextCustomCommand{
		Name:        row.Name,
		DisplayName: row.DisplayName,
		Description: row.Description,
		Usage:       usage,
		Type:        spec.Type,
		Aliases:     spec.Aliases,
		Labels:      map[string]string(row.Labels),
	}
	for _, param := range spec.Parameters {
		out.Parameters = append(out.Parameters, ParameterToProto(param))
	}
	return out, nil
}

// CanonicalUsage derives the full invocation shape from the parameter
// declarations. spec.usage remains a free-form example, but errors and /help
// use this line so a stale hint cannot teach the user the wrong flag.
func CanonicalUsage(name string, params []model.CommandParameterM) string {
	return commandusage.Line(name, usageParameters(params))
}

// CanonicalArguments is CanonicalUsage without the command name, matching the
// shape stored in spec.usage.
func CanonicalArguments(params []model.CommandParameterM) string {
	return commandusage.Arguments(usageParameters(params))
}

func usageParameters(params []model.CommandParameterM) []commandusage.Parameter {
	out := make([]commandusage.Parameter, 0, len(params))
	for i := range params {
		p := &params[i]
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

// ParameterToProto converts one declared parameter.
func ParameterToProto(p model.CommandParameterM) *v1.CommandParameter {
	return &v1.CommandParameter{
		Name:         p.Name,
		In:           p.In,
		Type:         p.Type,
		Description:  p.Description,
		Required:     p.Required,
		DefaultValue: string(p.Default),
		Enums:        p.Enum,
		Format:       p.Format,
		Short:        p.Short,
		SchemaJson:   string(p.Schema),
	}
}

// ParameterFromProto converts one declared parameter.
func ParameterFromProto(p *v1.CommandParameter) model.CommandParameterM {
	if p == nil {
		return model.CommandParameterM{}
	}
	out := model.CommandParameterM{
		Name:        p.GetName(),
		In:          p.GetIn(),
		Type:        p.GetType(),
		Description: p.GetDescription(),
		Required:    p.GetRequired(),
		Enum:        p.GetEnums(),
		Format:      p.GetFormat(),
		Short:       p.GetShort(),
	}
	if v := p.GetDefaultValue(); v != "" {
		out.Default = json.RawMessage(v)
	}
	if v := p.GetSchemaJson(); v != "" {
		out.Schema = json.RawMessage(v)
	}
	return out
}

// UserInfoFromProto mirrors the protobuf identity block onto the Kubernetes
// type templates see.
func UserInfoFromProto(p *v1.UserInfo) *authenticationv1.UserInfo {
	if p == nil {
		return nil
	}
	out := &authenticationv1.UserInfo{
		Username: p.GetUsername(),
		UID:      p.GetUid(),
		Groups:   p.GetGroups(),
	}
	if extra := p.GetExtra(); len(extra) > 0 {
		out.Extra = make(map[string]authenticationv1.ExtraValue, len(extra))
		for k, v := range extra {
			out.Extra[k] = authenticationv1.ExtraValue(v.GetValues())
		}
	}
	return out
}

func promptFromProto(p *v1.PromptCommand) *model.PromptCommandM {
	if p == nil {
		return nil
	}
	return &model.PromptCommandM{Template: p.GetTemplate()}
}

func promptToProto(p *model.PromptCommandM) *v1.PromptCommand {
	if p == nil {
		return nil
	}
	return &v1.PromptCommand{Template: p.Template}
}

func httpFromProto(p *v1.HTTPCommand) *model.HTTPCommandM {
	if p == nil {
		return nil
	}
	out := &model.HTTPCommandM{
		Method:                p.GetMethod(),
		URL:                   p.GetUrl(),
		CABundle:              p.GetCaBundle(),
		InsecureSkipTLSVerify: p.GetInsecureSkipTLSVerify(),
		Query:                 p.GetQuery(),
		Body:                  p.GetBody(),
		ContentType:           p.GetContentType(),
		SideEffects:           p.GetSideEffects(),
	}
	if svc := p.GetService(); svc != nil {
		out.Service = &model.ServiceReferenceM{
			Namespace: svc.GetNamespace(),
			Name:      svc.GetName(),
			Port:      svc.GetPort(),
			Path:      svc.GetPath(),
		}
	}
	for _, h := range p.GetHeaders() {
		out.Headers = append(out.Headers, model.HTTPHeaderM{Name: h.GetName(), Value: h.GetValue()})
	}
	if ref := p.GetSecretRef(); ref != nil {
		out.SecretRef = &model.ReferenceM{Name: ref.GetName(), Namespace: ref.GetNamespace()}
	}
	if cc := p.GetClientConfig(); cc != nil {
		// The row carries the CRD's JSON shape so the control-plane round trip
		// does not have to know the wire message.
		if data, err := json.Marshal(conversion.ClientConfigV1ToCR(cc)); err == nil {
			out.ClientConfig = data
		}
	}
	return out
}

func httpToProto(p *model.HTTPCommandM) *v1.HTTPCommand {
	if p == nil {
		return nil
	}
	out := &v1.HTTPCommand{
		Method:                p.Method,
		Url:                   p.URL,
		CaBundle:              p.CABundle,
		InsecureSkipTLSVerify: p.InsecureSkipTLSVerify,
		Query:                 p.Query,
		Body:                  p.Body,
		ContentType:           p.ContentType,
		SideEffects:           p.SideEffects,
	}
	if p.Service != nil {
		out.Service = &v1.ServiceReference{
			Namespace: p.Service.Namespace,
			Name:      p.Service.Name,
			Port:      p.Service.Port,
			Path:      p.Service.Path,
		}
	}
	for _, h := range p.Headers {
		out.Headers = append(out.Headers, &v1.HTTPHeader{Name: h.Name, Value: h.Value})
	}
	if p.SecretRef != nil {
		out.SecretRef = &v1.Reference{Name: p.SecretRef.Name, Namespace: p.SecretRef.Namespace}
	}
	if len(p.ClientConfig) > 0 {
		cfg := &agentv1alpha1.ClientConfig{}
		if err := json.Unmarshal(p.ClientConfig, cfg); err == nil {
			out.ClientConfig = conversion.ClientConfigFromCR(cfg)
		}
	}
	return out
}

func toolFromProto(p *v1.ToolCommand) *model.ToolCommandM {
	if p == nil {
		return nil
	}
	out := &model.ToolCommandM{Tool: p.GetTool(), ArgumentsTemplate: p.GetArgumentsTemplate()}
	if ref := p.GetMcpServer(); ref != nil {
		out.MCPServer = model.ReferenceM{Name: ref.GetName(), Namespace: ref.GetNamespace()}
	}
	return out
}

func toolToProto(p *model.ToolCommandM) *v1.ToolCommand {
	if p == nil {
		return nil
	}
	return &v1.ToolCommand{
		McpServer:         &v1.Reference{Name: p.MCPServer.Name, Namespace: p.MCPServer.Namespace},
		Tool:              p.Tool,
		ArgumentsTemplate: p.ArgumentsTemplate,
	}
}

func outputFromProto(p *v1.CommandOutput) *model.CommandOutputM {
	if p == nil {
		return nil
	}
	out := &model.CommandOutputM{Format: p.GetFormat(), Template: p.GetTemplate()}
	if v := p.GetSchemaJson(); v != "" {
		out.Schema = json.RawMessage(v)
	}
	return out
}

func outputToProto(p *model.CommandOutputM) *v1.CommandOutput {
	if p == nil {
		return nil
	}
	return &v1.CommandOutput{
		Format:     p.Format,
		Template:   p.Template,
		SchemaJson: string(p.Schema),
	}
}

func protoStatus(enabled bool) string {
	if enabled {
		return ""
	}
	return model.StatusDisabled
}
