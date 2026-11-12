package kbtool

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/contextx"
)

// TestAuditReceivesWhatWasRendered pins the audit's unit of account: it names
// the documents and the person, and its byte count describes the text the model
// received rather than everything the retrievers returned.
func TestAuditReceivesWhatWasRendered(t *testing.T) {
	var got []AuditEntry
	tool := New(Config{
		Retrievers: []Retriever{&fakeRetriever{name: "kb", docs: []Document{
			{Content: "alpha passage", KB: "kb-course", Source: "doc-1", Score: 1},
			{Content: "beta passage", KB: "kb-course", Source: "doc-2", Score: 0.5},
		}}},
		Audit: func(e AuditEntry) { got = append(got, e) },
	}).(*retrievalTool)

	ctx := authz.ContextWithSubject(context.Background(), authz.SubjectFromUser("u-1", ""))
	ctx = contextx.WithAgentNames(ctx, []string{"onex-u-1"})

	_, err := tool.InvokableRun(ctx, `{"query":"q"}`)
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, "u-1", got[0].UserID)
	assert.Equal(t, "onex-u-1", got[0].Agent)
	assert.Equal(t, "kb-course", got[0].KnowledgeBases)
	assert.Equal(t, 2, got[0].Documents)
	assert.Equal(t, 2, got[0].Passages)
	assert.Equal(t, len("alpha passage")+len("beta passage"), got[0].Bytes)
}
