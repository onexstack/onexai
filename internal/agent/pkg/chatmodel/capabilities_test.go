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

package chatmodel

import (
	"reflect"
	"testing"
)

// TestDefaultForInference pins the vendor inference a row that declares nothing
// falls back to. DeepSeek is not one answer: v4 and the vl family are
// multimodal, older chat/reasoner names are not, and everything else keeps the
// historical "assume vision" answer.
func TestDefaultForInference(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     bool
	}{
		{name: "deepseek v4 is multimodal", model: "deepseek-v4-flash", want: true},
		{name: "the deepseek vl family sees images", model: "deepseek-vl-7b", want: true},
		{name: "older deepseek stays text-only", model: "deepseek-chat", want: false},
		{name: "a deepseek model by provider", provider: "deepseek", model: "anything", want: false},
		{name: "an openai-named model", model: "gpt-4o-mini", want: true},
		{name: "a claude model", model: "claude-sonnet-5", want: true},
		{name: "an unknown name", model: "some-local-model", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultFor(tt.provider, tt.model).Has(CapVision)
			if got != tt.want {
				t.Errorf("DefaultFor(%q,%q).Has(vision) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

// TestResolvePrefersTheDeclaration pins both directions of the override, which
// is the reason the field exists: the name is a statement about a vendor, and
// the row is a statement about this endpoint.
func TestResolvePrefersTheDeclaration(t *testing.T) {
	tests := []struct {
		name     string
		declared []string
		provider string
		model    string
		want     bool
	}{
		{
			name:     "a deepseek name that declares vision can see",
			declared: []string{"vision"}, provider: "deepseek", model: "deepseek-v4-flash",
			want: true,
		},
		{
			name:     "an openai name whose row declares no vision cannot",
			declared: []string{"image-generation"}, provider: "openai", model: "gpt-4o-mini",
			want: false,
		},
		{
			name:     "an empty declaration falls back to the name",
			declared: nil, provider: "deepseek", model: "deepseek-v4-flash",
			want: true,
		},
		{
			name:     "an explicit text-only declaration beats a multimodal name",
			declared: []string{"text-only"}, provider: "deepseek", model: "deepseek-v4-flash",
			want: false,
		},
		{
			name:     "an empty declaration falls back the other way too",
			declared: []string{}, provider: "", model: "gpt-4o-mini",
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.declared, tt.provider, tt.model).Has(CapVision)
			if got != tt.want {
				t.Errorf("Resolve(%v,%q,%q).Has(vision) = %v, want %v", tt.declared, tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

// TestParseKeepsUnknownNames pins that a capability a newer version wrote is not
// erased by an older version reading and writing the row back: a round trip
// through this build must not lose another build's declaration.
func TestParseKeepsUnknownNames(t *testing.T) {
	caps := Parse([]string{"vision", "  ", "a-capability-from-the-future"})
	if !caps.Has(CapVision) {
		t.Error("the known capability was dropped")
	}
	if !caps.Has("a-capability-from-the-future") {
		t.Error("an unknown capability was dropped, so a round trip would erase it")
	}
	if got := caps.Names(); !reflect.DeepEqual(got, []string{"vision", "a-capability-from-the-future"}) {
		t.Errorf("Names() = %v, want the known name first then the unknown one", got)
	}
	if Parse(nil) != nil || Parse([]string{"", "  "}) != nil {
		t.Error("an empty declaration should parse to a nil set, not an empty one")
	}
}

// TestNamesIsStable pins that the order a set renders in does not depend on Go's
// map iteration order: a stored value that reorders itself on every save is a
// row that looks edited when nothing changed.
func TestNamesIsStable(t *testing.T) {
	caps := Parse([]string{"image-generation", "zzz", "aaa", "vision"})
	want := []string{"vision", "image-generation", "aaa", "zzz"}

	for i := 0; i < 20; i++ {
		if got := caps.Names(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Names() = %v, want %v (iteration %d)", got, want, i)
		}
	}
	if got := (Capabilities(nil)).Names(); got != nil {
		t.Errorf("Names() of a nil set = %v, want nil", got)
	}
}
