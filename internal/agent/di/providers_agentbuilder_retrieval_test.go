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

package di

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/pkg/options"
)

// TestKBToolSource_ProbeUsesRecordKnobs pins that a health probe runs with the
// record's own retrieval setting: the probe is the one search an operator uses
// to judge a dataset, so it must not silently use a different threshold.
func TestKBToolSource_ProbeUsesRecordKnobs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		threshold  float64
		deployment float64
		wantOnWire string
	}{
		{name: "record wins", threshold: 0.35, deployment: 0.1, wantOnWire: `"similarity_threshold":0.35`},
		{name: "deployment fallback", threshold: 0, deployment: 0.1, wantOnWire: `"similarity_threshold":0.1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buf, _ := io.ReadAll(r.Body)
				gotBody = string(buf)
				fmt.Fprint(w, `{"code":0,"data":{"chunks":[]}}`)
			}))
			defer srv.Close()

			source := kbToolSource{ragflow: options.RAGFlowOptions{
				BaseURL:             srv.URL,
				APIKey:              "k",
				SimilarityThreshold: tc.deployment,
				TopK:                5,
			}}
			kb := &model.KnowledgeBaseM{
				Name:           "kb",
				DisplayName:    "KB",
				Platform:       model.PlatformRAGFlow,
				DatasetID:      "ds-1",
				Status:         model.StatusEnabled,
				ScoreThreshold: tc.threshold,
				TopK:           4,
			}
			if _, err := source.Probe(context.Background(), kb); err != nil {
				t.Fatalf("Probe: %v", err)
			}
			for _, want := range []string{tc.wantOnWire, `"page_size":1`, `"ds-1"`} {
				if !strings.Contains(gotBody, want) {
					t.Errorf("probe request is missing %s: %s", want, gotBody)
				}
			}
		})
	}
}
