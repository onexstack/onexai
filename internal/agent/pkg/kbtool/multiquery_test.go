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

package kbtool

import (
	"slices"
	"testing"
)

func doc(kb, key, content string, score float64) Document {
	return Document{KB: kb, DocumentKey: key, Content: content, Score: score}
}

// TestFuseRRFAgreesAcrossLists pins the property fusion exists for: a passage
// several phrasings found outranks a passage only one did, even when that one
// list put it first.
func TestFuseRRFAgreesAcrossLists(t *testing.T) {
	lists := [][]Document{
		{doc("kb", "d1", "first of list one", 0.9), doc("kb", "shared", "found twice", 0.5)},
		{doc("kb", "shared", "found twice", 0.5), doc("kb", "d2", "second list only", 0.8)},
	}

	got := fuseRRF(lists, 0)
	if len(got) != 3 {
		t.Fatalf("fused %d passages, want 3 (one duplicate must collapse): %v", len(got), got)
	}
	if got[0].Content != "found twice" {
		t.Errorf("first passage is %q, want the one both lists found", got[0].Content)
	}
}

// TestFuseRRFDeduplicatesWithinAList pins that a platform returning the same
// passage twice does not get double weight.
func TestFuseRRFDeduplicatesWithinAList(t *testing.T) {
	lists := [][]Document{{
		doc("kb", "d1", "same text", 0.9),
		doc("kb", "d1", "same text", 0.9),
		doc("kb", "d2", "other", 0.1),
	}}
	got := fuseRRF(lists, 0)
	if len(got) != 2 {
		t.Fatalf("fused %d passages, want 2: %v", len(got), got)
	}
	if got[0].Content != "same text" {
		t.Errorf("first passage is %q, want the duplicated one", got[0].Content)
	}
}

// TestFuseRRFTrimsToTopK pins the global cap.
func TestFuseRRFTrimsToTopK(t *testing.T) {
	lists := [][]Document{{
		doc("kb", "d1", "a", 0.9),
		doc("kb", "d2", "b", 0.8),
		doc("kb", "d3", "c", 0.7),
	}}
	got := fuseRRF(lists, 2)
	if len(got) != 2 {
		t.Fatalf("fused %d passages, want 2", len(got))
	}
}

func TestParseRewrites(t *testing.T) {
	reply := "1. How do I reset a Kubernetes cluster?\n" +
		"- kubernetes cluster reset procedure\n" +
		"* 如何重置 k8s 集群\n" +
		"\"How do I reset a Kubernetes cluster?\"\n" +
		"\n" +
		"how to reset kubernetes cluster\n" +
		"extra line beyond the limit\n"

	got := parseRewrites(reply, "How do I reset a Kubernetes cluster?", 3)

	want := []string{
		"kubernetes cluster reset procedure",
		"如何重置 k8s 集群",
		"how to reset kubernetes cluster",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("parseRewrites = %q, want %q", got, want)
	}
}

func TestSampledIsStablePerRun(t *testing.T) {
	// A rate of 1 is unconditional, and 0 is the documented "every search".
	for _, rate := range []float64{0, 1, -0.5} {
		if !sampled("run-1", rate) {
			t.Fatalf("rate %v sampled out", rate)
		}
	}
	// A partial rate must not flip between calls for the same run: a retried
	// tool call has to stay in the arm it started in.
	first := sampled("run-42", 0.5)
	for i := 0; i < 20; i++ {
		if sampled("run-42", 0.5) != first {
			t.Fatal("the sampling decision is not stable for one run")
		}
	}
	// And across many runs both arms must be reachable.
	in, out := 0, 0
	for i := 0; i < 200; i++ {
		if sampled(string(rune(i)), 0.5) {
			in++
		} else {
			out++
		}
	}
	if in == 0 || out == 0 {
		t.Fatalf("a 50%% rate produced only one arm (%d in, %d out)", in, out)
	}
}
