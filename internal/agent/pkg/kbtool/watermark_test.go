package kbtool

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/kbfingerprint"
)

var testWatermarkKey = []byte("0123456789abcdef0123456789abcdef")

// TestWatermarkNamesTheAskingUser is the property cmd/ox-kbtrace depends on:
// a passage served to a user carries that user's mark, and the decoder finds it
// in an excerpt rather than needing the whole document.
func TestWatermarkNamesTheAskingUser(t *testing.T) {
	ctx := authz.ContextWithSubject(context.Background(), authz.SubjectFromUser("u-9f2c1a", ""))
	docs := []Document{{
		Content: strings.Repeat("一段足够长的正文，里面有空格 可以 放置 标记。", 200),
		KB:      "kb-course",
		Source:  "kbn-node",
	}}

	out := watermarkPassages(ctx, docs, testWatermarkKey)
	require.Len(t, out, 1)
	assert.NotEqual(t, docs[0].Content, out[0].Content, "nothing was written, so this proves nothing")

	marks := kbfingerprint.Extract(out[0].Content)
	require.NotEmpty(t, marks, "the served passage carries no mark")
	assert.Equal(t, kbfingerprint.Of(testWatermarkKey, "u-9f2c1a"), marks[0])
}

// TestWatermarkSkipsCallersWithNoPerson keeps a service-account run from
// writing the platform's own name into a copy: there is no person to attribute
// it to, and a false attribution is worse than none.
func TestWatermarkSkipsCallersWithNoPerson(t *testing.T) {
	ctx := authz.ContextWithSubject(context.Background(), authz.SubjectFromService("onex-agent-apiserver"))
	docs := []Document{{Content: "some text with spaces in it", KB: "kb", Source: "doc"}}

	out := watermarkPassages(ctx, docs, testWatermarkKey)
	assert.Equal(t, docs[0].Content, out[0].Content)
}

// TestWatermarkOffChangesNothing pins the default: no key means the passages
// are returned exactly as the retriever produced them.
func TestWatermarkOffChangesNothing(t *testing.T) {
	ctx := authz.ContextWithSubject(context.Background(), authz.SubjectFromUser("u-1", ""))
	docs := []Document{{Content: "some text with spaces in it", KB: "kb", Source: "doc"}}
	assert.Equal(t, docs[0].Content, watermarkPassages(ctx, docs, nil)[0].Content)
}
