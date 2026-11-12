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

package dutyservice

import (
	"regexp"
	"strings"
)

// compileFilter compiles an optional case-insensitive filter. A nil pattern
// with ok true means "match everything"; ok false means the pattern was
// invalid and the caller must surface that to the user.
func compileFilter(filter string) (*regexp.Regexp, bool) {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return nil, true
	}
	re, err := regexp.Compile("(?i)" + filter)
	if err != nil {
		return nil, false
	}
	return re, true
}
