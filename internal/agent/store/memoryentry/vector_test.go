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

package memoryentry

import (
	"reflect"
	"testing"
)

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"0.8.0", true},
		{"0.8.6", true},
		{"0.9", true},
		{"1.0.0", true},
		{"0.7.4", false},
		{"0.8", true}, // 0.8 is 0.8.0
		{"", false},
		{"garbage", false},
	}
	for _, tc := range cases {
		if got := versionAtLeast(tc.version, 0, 8, 0); got != tc.want {
			t.Errorf("versionAtLeast(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestLikeTerms(t *testing.T) {
	got := likeTerms([]string{" Database ", "a", "", "100%", "a_b", `c\d`})
	want := []string{"Database", `100\%`, `a\_b`, `c\\d`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("likeTerms = %v, want %v", got, want)
	}
}

func TestMergeIDs(t *testing.T) {
	got := mergeIDs([]string{"a", "b", ""}, []string{"b", "c"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeIDs = %v, want %v", got, want)
	}
}
