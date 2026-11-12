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

import (
	"reflect"
	"testing"
)

// TestParseRegenUserMap pins the accepted spellings of the Regen on-call name
// to Lark user_id map. It is a plain string on the options struct rather than a
// map because every other backend option has a flag, and pflag has no map
// primitive; the parsing rules are therefore the interface, and a typo has to
// be an error rather than a silently dropped entry.
func TestParseRegenUserMap(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "empty means no mapping",
			raw:  "",
			want: map[string]string{},
		},
		{
			name: "comma separated pairs",
			raw:  "go.kong@example.com=go.kong,toka.wu@example.com=toka.wu",
			want: map[string]string{
				"go.kong@example.com": "go.kong",
				"toka.wu@example.com": "toka.wu",
			},
		},
		{
			name: "semicolons, spaces and a trailing separator are tolerated",
			raw:  " go.kong@example.com = go.kong ; toka.wu@example.com=toka.wu, ",
			want: map[string]string{
				"go.kong@example.com": "go.kong",
				"toka.wu@example.com": "toka.wu",
			},
		},
		{
			name:    "a pair without a separator is an error",
			raw:     "go.kong@example.com",
			wantErr: true,
		},
		{
			name:    "an empty side is an error",
			raw:     "go.kong@example.com=",
			wantErr: true,
		},
		{
			name:    "an empty name is an error",
			raw:     "=go.kong",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &BackendOptions{RegenUserMap: tt.raw}
			got, err := o.ParseRegenUserMap()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseRegenUserMap(%q) = %v, want an error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRegenUserMap(%q) returned error: %v", tt.raw, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseRegenUserMap(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
