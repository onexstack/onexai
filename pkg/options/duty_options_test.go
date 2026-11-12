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

package options

import "testing"

func TestEffectiveProvider(t *testing.T) {
	tests := []struct {
		name    string
		opts    *DutyOptions
		backend *BackendOptions
		want    string
	}{
		{name: "nil options", opts: nil, backend: nil, want: "none"},
		{name: "empty without endpoint", opts: NewDutyOptions(), backend: &BackendOptions{}, want: "none"},
		{name: "empty with endpoint", opts: NewDutyOptions(), backend: &BackendOptions{RegenEndpoint: "http://regen"}, want: "regen"},
		{name: "explicit none", opts: &DutyOptions{Provider: " NONE "}, backend: &BackendOptions{RegenEndpoint: "http://regen"}, want: "none"},
		{name: "explicit regen", opts: &DutyOptions{Provider: "regen"}, backend: nil, want: "regen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.opts.EffectiveProvider(tt.backend); got != tt.want {
				t.Fatalf("EffectiveProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}
