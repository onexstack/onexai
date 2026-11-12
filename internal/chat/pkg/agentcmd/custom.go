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

package agentcmd

import (
	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// CustomCommandFromProto converts a full command definition (the CRUD list the
// MCP bridge reads at startup) into the runtime shape.
func CustomCommandFromProto(c *agentv1.CustomCommand) runtime.CustomCommand {
	if c == nil {
		return runtime.CustomCommand{}
	}
	return customCommand(
		c.GetName(), c.GetDisplayName(), c.GetDescription(), c.GetUsage(), c.GetType(),
		c.GetAliases(), c.GetLabels(), c.GetParameters(), "",
	)
}

// ContextCustomCommandFromProto converts the per-agent summary.
func ContextCustomCommandFromProto(c *agentv1.ContextCustomCommand, agent string) runtime.CustomCommand {
	if c == nil {
		return runtime.CustomCommand{}
	}
	return customCommand(
		c.GetName(), c.GetDisplayName(), c.GetDescription(), c.GetUsage(), c.GetType(),
		c.GetAliases(), c.GetLabels(), c.GetParameters(), agent,
	)
}

func customCommand(
	name, displayName, description, usage, kind string,
	aliases []string, labels map[string]string,
	params []*agentv1.CommandParameter, agent string,
) runtime.CustomCommand {
	out := runtime.CustomCommand{
		Name:        name,
		DisplayName: displayName,
		Description: description,
		Usage:       usage,
		Type:        kind,
		Aliases:     aliases,
		Agent:       agent,
		Labels:      labels,
	}
	for _, p := range params {
		out.Parameters = append(out.Parameters, runtime.CustomCommandParameter{
			Name:        p.GetName(),
			In:          p.GetIn(),
			Type:        p.GetType(),
			Description: p.GetDescription(),
			Required:    p.GetRequired(),
			DefaultJSON: p.GetDefaultValue(),
			Enum:        p.GetEnums(),
			Format:      p.GetFormat(),
			Short:       p.GetShort(),
			SchemaJSON:  p.GetSchemaJson(),
		})
	}
	return out
}
