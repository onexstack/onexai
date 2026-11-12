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

package larkcard

import (
	"strconv"
	"strings"
	"time"
)

// Unit thresholds for FormatTokens.
const (
	thousand = 1000
	million  = 1000 * thousand
)

// FormatTokens renders a token count for a card, where space is tight and the
// exact figure matters less than its magnitude: 820 stays 820, 21500 becomes
// 21.5k. A trailing ".0" is dropped, so a whole number of thousands reads as
// 2k rather than 2.0k.
//
// A non-positive count is rendered as "0". Callers are expected to omit the
// figure entirely rather than print it when the upstream reported no usage —
// zero here means "reported zero", not "not reported".
func FormatTokens(n int) string {
	if n <= 0 {
		return "0"
	}
	if n < thousand {
		return strconv.Itoa(n)
	}
	if n < million {
		// 999_950 and up round to 1000k, which reads as a unit error; report the
		// next unit up instead.
		if k := oneDecimal(float64(n) / thousand); k != "1000" {
			return k + "k"
		}
	}
	return oneDecimal(float64(n)/million) + "m"
}

// FormatDurationMs renders an elapsed time for a card: 234ms, 1.0s, 1m14s.
//
// Unlike FormatTokens this keeps a trailing ".0" (1030ms is "1.0s", not "1s"),
// because a duration is read as a measurement rather than as a magnitude.
func FormatDurationMs(ms int64) string {
	switch {
	case ms <= 0:
		return "0ms"
	case ms < thousand:
		return strconv.FormatInt(ms, 10) + "ms"
	case ms < int64(time.Minute/time.Millisecond):
		return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + "s"
	default:
		// Minutes and up are Go's own rendering ("1m14s", "1h0m0s"), so the hour
		// case does not need a branch of its own. Truncated to whole seconds:
		// past a minute the sub-second part is noise on a card.
		return (time.Duration(ms) * time.Millisecond).Truncate(time.Second).String()
	}
}

// oneDecimal renders v with one decimal place, dropping a trailing ".0".
func oneDecimal(v float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
}
