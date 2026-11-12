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

package conversion

import (
	"encoding/json"

	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// RuntimeProfileV1ToRuntimeProfileModel converts a proto runtime profile to the
// domain model.
func RuntimeProfileV1ToRuntimeProfileModel(p *v1.RuntimeProfile) *model.RuntimeProfileM {
	if p == nil {
		return &model.RuntimeProfileM{}
	}
	out := &model.RuntimeProfileM{
		Name:               p.GetName(),
		Namespace:          p.GetNamespace(),
		DisplayName:        p.GetDisplayName(),
		Status:             p.GetStatus(),
		Description:        p.GetDescription(),
		Owner:              p.GetOwner(),
		Kind:               p.GetKind(),
		Endpoint:           p.GetEndpoint(),
		HostingRef:         p.GetHostingRef(),
		ModelAccess:        p.GetModelAccess(),
		AuthBearerSecret:   p.GetAuthBearerSecret(),
		AuthBearerKey:      p.GetAuthBearerKey(),
		AuthCASecret:       p.GetAuthCASecret(),
		AuthCAKey:          p.GetAuthCAKey(),
		AuthInsecureVerify: p.GetAuthInsecureVerify(),
		RunTimeoutNanos:    p.GetRunTimeoutNanos(),
		MaxConcurrentRun:   p.GetMaxConcurrentRun(),
		MaxToolCalls:       p.GetMaxToolCalls(),
		ResolvedEndpoint:   p.GetResolvedEndpoint(),
		Capabilities:       p.GetCapabilities(),
		Labels:             p.GetLabels(),
		CreatedAt:          p.GetCreatedAt(),
		UpdatedAt:          p.GetUpdatedAt(),
	}
	if cc := p.GetClientConfig(); cc != nil {
		if data, err := json.Marshal(ClientConfigV1ToCR(cc)); err == nil {
			out.ClientConfigJSON = string(data)
		}
		if cc.GetUrl() != "" {
			out.Endpoint = cc.GetUrl()
		}
	}
	return out
}

// RuntimeProfileModelToRuntimeProfileV1 converts the domain model back to the
// wire shape.
func RuntimeProfileModelToRuntimeProfileV1(row *model.RuntimeProfileM) *v1.RuntimeProfile {
	if row == nil {
		return nil
	}
	out := &v1.RuntimeProfile{
		Name:               row.Name,
		Namespace:          row.Namespace,
		DisplayName:        row.DisplayName,
		Status:             StatusToV1(row.Status),
		Description:        row.Description,
		Owner:              row.Owner,
		Kind:               row.Kind,
		Endpoint:           row.Endpoint,
		HostingRef:         row.HostingRef,
		ModelAccess:        row.ModelAccess,
		AuthBearerSecret:   row.AuthBearerSecret,
		AuthBearerKey:      row.AuthBearerKey,
		AuthCASecret:       row.AuthCASecret,
		AuthCAKey:          row.AuthCAKey,
		AuthInsecureVerify: row.AuthInsecureVerify,
		RunTimeoutNanos:    row.RunTimeoutNanos,
		MaxConcurrentRun:   row.MaxConcurrentRun,
		MaxToolCalls:       row.MaxToolCalls,
		ResolvedEndpoint:   row.ResolvedEndpoint,
		Capabilities:       row.Capabilities,
		Labels:             row.Labels,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
	if row.ClientConfigJSON != "" {
		cfg := &agentv1alpha1.ClientConfig{}
		if err := json.Unmarshal([]byte(row.ClientConfigJSON), cfg); err == nil {
			out.ClientConfig = ClientConfigFromCR(cfg)
		}
	}
	return out
}

// RuntimeHostingV1ToRuntimeHostingModel converts a proto runtime hosting to the
// domain model.
func RuntimeHostingV1ToRuntimeHostingModel(p *v1.RuntimeHosting) *model.RuntimeHostingM {
	if p == nil {
		return &model.RuntimeHostingM{}
	}
	return &model.RuntimeHostingM{
		Name:         p.GetName(),
		Namespace:    p.GetNamespace(),
		DisplayName:  p.GetDisplayName(),
		Status:       p.GetStatus(),
		Description:  p.GetDescription(),
		Owner:        p.GetOwner(),
		ProfileRef:   p.GetProfileRef(),
		Mode:         p.GetMode(),
		Image:        p.GetImage(),
		Command:      model.StringList(p.GetCommand()),
		Env:          p.GetEnv(),
		MaxInstances: p.GetMaxInstances(),
		IdleTTLNanos: p.GetIdleTTLNanos(),
		Resources:    p.GetResources(),
		Instances:    p.GetInstances(),
		Labels:       p.GetLabels(),
		CreatedAt:    p.GetCreatedAt(),
		UpdatedAt:    p.GetUpdatedAt(),
	}
}

// RuntimeHostingModelToRuntimeHostingV1 converts the domain model back to the
// wire shape.
func RuntimeHostingModelToRuntimeHostingV1(row *model.RuntimeHostingM) *v1.RuntimeHosting {
	if row == nil {
		return nil
	}
	return &v1.RuntimeHosting{
		Name:         row.Name,
		Namespace:    row.Namespace,
		DisplayName:  row.DisplayName,
		Status:       StatusToV1(row.Status),
		Description:  row.Description,
		Owner:        row.Owner,
		ProfileRef:   row.ProfileRef,
		Mode:         row.Mode,
		Image:        row.Image,
		Command:      []string(row.Command),
		Env:          row.Env,
		MaxInstances: row.MaxInstances,
		IdleTTLNanos: row.IdleTTLNanos,
		Resources:    row.Resources,
		Instances:    row.Instances,
		Labels:       row.Labels,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

// SandboxTemplateV1ToSandboxTemplateModel converts a proto sandbox template to
// the domain model.
func SandboxTemplateV1ToSandboxTemplateModel(p *v1.SandboxTemplate) *model.SandboxTemplateM {
	if p == nil {
		return &model.SandboxTemplateM{}
	}
	return &model.SandboxTemplateM{
		Name:        p.GetName(),
		Namespace:   p.GetNamespace(),
		DisplayName: p.GetDisplayName(),
		Description: p.GetDescription(),
		Status:      p.GetStatus(),
		Image:       p.GetImage(),
		Command:     model.StringList(p.GetCommand()),
		Env:         model.StringList(p.GetEnv()),
		TTLSeconds:  p.GetTtlSeconds(),
		Labels:      p.GetLabels(),
		CreatedAt:   p.GetCreatedAt(),
		UpdatedAt:   p.GetUpdatedAt(),
	}
}

// SandboxTemplateModelToSandboxTemplateV1 converts the domain model back to the
// wire shape.
func SandboxTemplateModelToSandboxTemplateV1(row *model.SandboxTemplateM) *v1.SandboxTemplate {
	if row == nil {
		return nil
	}
	return &v1.SandboxTemplate{
		Name:        row.Name,
		Namespace:   row.Namespace,
		DisplayName: row.DisplayName,
		Description: row.Description,
		Status:      StatusToV1(row.Status),
		Image:       row.Image,
		Command:     []string(row.Command),
		Env:         []string(row.Env),
		TtlSeconds:  row.TTLSeconds,
		Labels:      row.Labels,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

// ScheduledRunV1ToScheduledRunModel converts a proto scheduled run to the
// domain model.
func ScheduledRunV1ToScheduledRunModel(p *v1.ScheduledRun) *model.ScheduledRunM {
	if p == nil {
		return &model.ScheduledRunM{}
	}
	return &model.ScheduledRunM{
		Name:                  p.GetName(),
		Namespace:             p.GetNamespace(),
		DisplayName:           p.GetDisplayName(),
		Description:           p.GetDescription(),
		Status:                p.GetStatus(),
		Owner:                 p.GetOwner(),
		CreatedBy:             p.GetCreatedBy(),
		Agent:                 p.GetAgent(),
		Schedule:              p.GetSchedule(),
		TimeZone:              p.GetTimeZone(),
		Prompt:                p.GetPrompt(),
		Paused:                p.GetPaused(),
		ExecutionTimeoutNanos: p.GetExecutionTimeoutNanos(),
		LastExecutionID:       p.GetLastExecutionId(),
		LastScheduledAt:       p.GetLastScheduledAt(),
		Labels:                p.GetLabels(),
		CreatedAt:             p.GetCreatedAt(),
		UpdatedAt:             p.GetUpdatedAt(),
	}
}

// ScheduledRunModelToScheduledRunV1 converts the domain model back to the wire
// shape.
func ScheduledRunModelToScheduledRunV1(row *model.ScheduledRunM) *v1.ScheduledRun {
	if row == nil {
		return nil
	}
	return &v1.ScheduledRun{
		Name:                  row.Name,
		Namespace:             row.Namespace,
		DisplayName:           row.DisplayName,
		Description:           row.Description,
		Status:                StatusToV1(row.Status),
		Owner:                 row.Owner,
		CreatedBy:             row.CreatedBy,
		Agent:                 row.Agent,
		Schedule:              row.Schedule,
		TimeZone:              row.TimeZone,
		Prompt:                row.Prompt,
		Paused:                row.Paused,
		ExecutionTimeoutNanos: row.ExecutionTimeoutNanos,
		LastExecutionId:       row.LastExecutionID,
		LastScheduledAt:       row.LastScheduledAt,
		NextScheduledAt:       row.NextScheduledAt,
		Labels:                row.Labels,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
}
