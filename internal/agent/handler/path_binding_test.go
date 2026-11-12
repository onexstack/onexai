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

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/onexstack/onexai/internal/pkg/restbind"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// TestPathParamsBindFromURI pins the contract between the route parameter names
// and the `@gotags: uri:"..."` tags on the request messages.
//
// A missing or misspelt tag does not fail to compile and does not raise a bind
// error: gin leaves the field empty and the request dies later with
// "name is required". These cases turn that silent 400 into a test failure.
func TestPathParamsBindFromURI(t *testing.T) {
	tests := []struct {
		caseName string
		params   gin.Params
		rq       proto.Message
		field    protoreflect.Name
		want     string
	}{
		{"GetAgentRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetAgentRequest{}, "name", "a"},
		{"DeleteAgentRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteAgentRequest{}, "name", "a"},
		{"GetAgentA2AInvocationRequest.name", gin.Params{{Key: "name", Value: "a"}}, &v1.GetAgentA2AInvocationRequest{}, "name", "a"},
		{"GetAgentA2AInvocationRequest.invocationId", gin.Params{{Key: "invocationId", Value: "inv"}}, &v1.GetAgentA2AInvocationRequest{}, "invocationId", "inv"},
		{"ListAgentA2AInvocationRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.ListAgentA2AInvocationRequest{}, "name", "a"},
		{"GetAgentBindingRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetAgentBindingRequest{}, "name", "a"},
		{"DeleteAgentBindingRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteAgentBindingRequest{}, "name", "a"},
		{"GetAgentTemplateRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetAgentTemplateRequest{}, "name", "a"},
		{"DeleteAgentTemplateRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteAgentTemplateRequest{}, "name", "a"},
		{"GetAuthzRuleRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetAuthzRuleRequest{}, "name", "a"},
		{"DeleteAuthzRuleRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteAuthzRuleRequest{}, "name", "a"},
		{"GetClusterAgentBindingRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetClusterAgentBindingRequest{}, "name", "a"},
		{"DeleteClusterAgentBindingRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteClusterAgentBindingRequest{}, "name", "a"},
		{"GetCustomCommandRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetCustomCommandRequest{}, "name", "a"},
		{"DeleteCustomCommandRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteCustomCommandRequest{}, "name", "a"},
		{"GetKnowledgeBaseRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetKnowledgeBaseRequest{}, "name", "a"},
		{"DeleteKnowledgeBaseRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteKnowledgeBaseRequest{}, "name", "a"},
		{"GetMCPRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetMCPRequest{}, "name", "a"},
		{"DeleteMCPRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteMCPRequest{}, "name", "a"},
		{"GetMemoryRequest", gin.Params{{Key: "memory", Value: "m"}}, &v1.GetMemoryRequest{}, "name", "m"},
		{"DeleteMemoryRequest", gin.Params{{Key: "memory", Value: "m"}}, &v1.DeleteMemoryRequest{}, "name", "m"},
		{"GetMemoryEntryRequest.memory", gin.Params{{Key: "memory", Value: "m"}}, &v1.GetMemoryEntryRequest{}, "memory", "m"},
		{"GetMemoryEntryRequest.id", gin.Params{{Key: "id", Value: "e"}}, &v1.GetMemoryEntryRequest{}, "id", "e"},
		{"DeleteMemoryEntryRequest.memory", gin.Params{{Key: "memory", Value: "m"}}, &v1.DeleteMemoryEntryRequest{}, "memory", "m"},
		{"DeleteMemoryEntryRequest.id", gin.Params{{Key: "id", Value: "e"}}, &v1.DeleteMemoryEntryRequest{}, "id", "e"},
		{"PurgeMemorySubjectRequest.memory", gin.Params{{Key: "memory", Value: "m"}}, &v1.PurgeMemorySubjectRequest{}, "memory", "m"},
		{"PurgeMemorySubjectRequest.subject", gin.Params{{Key: "subject", Value: "s"}}, &v1.PurgeMemorySubjectRequest{}, "subject", "s"},
		{"GetModelRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetModelRequest{}, "name", "a"},
		{"DeleteModelRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteModelRequest{}, "name", "a"},
		{"GetNamespaceRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetNamespaceRequest{}, "name", "a"},
		{"DeleteNamespaceRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteNamespaceRequest{}, "name", "a"},
		{"GetPromptRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetPromptRequest{}, "name", "a"},
		{"DeletePromptRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeletePromptRequest{}, "name", "a"},
		{"GetReferenceGrantRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetReferenceGrantRequest{}, "name", "a"},
		{"DeleteReferenceGrantRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteReferenceGrantRequest{}, "name", "a"},
		{"GetRunRequest", gin.Params{{Key: "id", Value: "r"}}, &v1.GetRunRequest{}, "id", "r"},
		{"CancelRunRequest", gin.Params{{Key: "id", Value: "r"}}, &v1.CancelRunRequest{}, "id", "r"},
		{"GetRuntimeHostingRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetRuntimeHostingRequest{}, "name", "a"},
		{"DeleteRuntimeHostingRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteRuntimeHostingRequest{}, "name", "a"},
		{"GetRuntimeProfileRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetRuntimeProfileRequest{}, "name", "a"},
		{"DeleteRuntimeProfileRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteRuntimeProfileRequest{}, "name", "a"},
		{"GetSandboxRequest", gin.Params{{Key: "id", Value: "s"}}, &v1.GetSandboxRequest{}, "id", "s"},
		{"DeleteSandboxRequest", gin.Params{{Key: "id", Value: "s"}}, &v1.DeleteSandboxRequest{}, "id", "s"},
		{"ReadSandboxFileRequest", gin.Params{{Key: "id", Value: "s"}}, &v1.ReadSandboxFileRequest{}, "id", "s"},
		{"WriteSandboxFileRequest", gin.Params{{Key: "id", Value: "s"}}, &v1.WriteSandboxFileRequest{}, "id", "s"},
		{"ExecSandboxRequest", gin.Params{{Key: "id", Value: "s"}}, &v1.ExecSandboxRequest{}, "id", "s"},
		{"InvokeAgentA2ARequest", gin.Params{{Key: "name", Value: "a"}}, &v1.InvokeAgentA2ARequest{}, "name", "a"},
		{"TestKnowledgeBaseRetrievalRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.TestKnowledgeBaseRetrievalRequest{}, "name", "a"},
		{"EvalMemoryRequest", gin.Params{{Key: "memory", Value: "m"}}, &v1.EvalMemoryRequest{}, "memory", "m"},
		{"GetSandboxTemplateRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetSandboxTemplateRequest{}, "name", "a"},
		{"DeleteSandboxTemplateRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteSandboxTemplateRequest{}, "name", "a"},
		{"TriggerScheduledRunRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.TriggerScheduledRunRequest{}, "name", "a"},
		{"CancelScheduledExecutionRequest.name", gin.Params{{Key: "name", Value: "a"}}, &v1.CancelScheduledExecutionRequest{}, "name", "a"},
		{"CancelScheduledExecutionRequest.id", gin.Params{{Key: "id", Value: "x"}}, &v1.CancelScheduledExecutionRequest{}, "id", "x"},
		{"GetScheduledRunRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetScheduledRunRequest{}, "name", "a"},
		{"DeleteScheduledRunRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteScheduledRunRequest{}, "name", "a"},
		{"GetSecretRequest", gin.Params{{Key: "secretId", Value: "sec"}}, &v1.GetSecretRequest{}, "secretId", "sec"},
		{"DeleteSecretRequest", gin.Params{{Key: "secretId", Value: "sec"}}, &v1.DeleteSecretRequest{}, "secretId", "sec"},
		{"ListSessionShareRequest", gin.Params{{Key: "id", Value: "sess"}}, &v1.ListSessionShareRequest{}, "id", "sess"},
		{"RevokeSessionShareRequest.id", gin.Params{{Key: "id", Value: "sess"}}, &v1.RevokeSessionShareRequest{}, "id", "sess"},
		{"RevokeSessionShareRequest.shareId", gin.Params{{Key: "shareID", Value: "sh"}}, &v1.RevokeSessionShareRequest{}, "shareId", "sh"},
		{"GetSkillRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetSkillRequest{}, "name", "a"},
		{"DeleteSkillRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteSkillRequest{}, "name", "a"},
		{"GetWebhookRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.GetWebhookRequest{}, "name", "a"},
		{"DeleteWebhookRequest", gin.Params{{Key: "name", Value: "a"}}, &v1.DeleteWebhookRequest{}, "name", "a"},
	}

	for _, tt := range tests {
		t.Run(tt.caseName, func(t *testing.T) {
			c, _ := newRecorder(t)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			c.Params = tt.params

			require.NoError(t, c.ShouldBindUri(tt.rq))
			assert.Equal(t, tt.want, protoStringField(t, tt.rq, tt.field))
		})
	}
}

// TestSandboxPathAndQueryBind covers the sandbox requests, whose owner and file
// path travel in the query string while the id is in the path. A query field
// that spells the identity field's Go name must not override the path.
func TestSandboxPathAndQueryBind(t *testing.T) {
	c, _ := newRecorder(t)
	c.Request = httptest.NewRequest(http.MethodGet, "/?owner=svc&path=/tmp/x&Id=other", nil)
	c.Params = gin.Params{{Key: "id", Value: "sb"}}

	var rq v1.ReadSandboxFileRequest
	require.NoError(t, restbind.URIAndQuery[v1.ReadSandboxFileRequest](c)(&rq))

	assert.Equal(t, "sb", rq.GetId())
	assert.Equal(t, "svc", rq.GetOwner())
	assert.Equal(t, "/tmp/x", rq.GetPath())
}

// TestPathScopedListKeepsPathIdentity covers the path+query list endpoints: the
// path names the parent, the query carries filters, and the path must win.
func TestPathScopedListKeepsPathIdentity(t *testing.T) {
	c, _ := newRecorder(t)
	c.Request = httptest.NewRequest(http.MethodGet, "/?Name=other&limit=5", nil)
	c.Params = gin.Params{{Key: "name", Value: "a"}}

	var rq v1.ListAgentA2AInvocationRequest
	require.NoError(t, restbind.URIAndQuery[v1.ListAgentA2AInvocationRequest](c)(&rq))

	assert.Equal(t, "a", rq.GetName())
	assert.Equal(t, int32(5), rq.GetLimit())
}

// TestMemoryEntryBindsPathAndSubject covers the one request that is filled from
// the path plus a value the handler computes (the caller's subject).
func TestMemoryEntryBindsPathAndSubject(t *testing.T) {
	c, _ := newRecorder(t)
	c.Request = httptest.NewRequest(http.MethodGet, "/?subject=s1", nil)
	c.Params = gin.Params{{Key: "memory", Value: "m"}, {Key: "id", Value: "e"}}

	var rq v1.GetMemoryEntryRequest
	binder := restbind.URIWith(c, func(r *v1.GetMemoryEntryRequest) {
		r.Subject = "from-claims"
	})
	require.NoError(t, binder(&rq))

	assert.Equal(t, "m", rq.GetMemory())
	assert.Equal(t, "e", rq.GetId())
	assert.Equal(t, "from-claims", rq.GetSubject())
}

// protoStringField reads a string field through protobuf reflection so the case
// table above stays one line per route parameter.
func protoStringField(t *testing.T, rq proto.Message, field protoreflect.Name) string {
	t.Helper()
	m := rq.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(field)
	require.NotNil(t, fd, "field %q does not exist on %s", field, m.Descriptor().FullName())
	return m.Get(fd).String()
}
