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

package ratelimit

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The counter itself needs Redis; what can be tested without it is the shape of
// the decision — which calls are counted, which are exempt, and what happens
// when the quota is not configured. The Redis path is exercised end to end by
// the service's own tests.

func TestNew_NoQuotaOrNoStoreIsNil(t *testing.T) {
	cases := map[string]*Limiter{
		"no limit": New(nil, "p", 0),
		"negative": New(nil, "p", -1),
		"no redis": New(nil, "p", 50),
	}
	for name, l := range cases {
		if l != nil {
			t.Errorf("%s: expected a nil limiter, got %+v", name, l)
		}
	}
}

// TestNilLimiterAllowsEverything pins that an unconfigured quota costs a call
// site nothing but the call: no nil check, and no behaviour change.
func TestNilLimiterAllowsEverything(t *testing.T) {
	var l *Limiter
	ok, remaining, err := l.Allow(context.Background(), "go.kong")
	if !ok || err != nil {
		t.Fatalf("Allow = (%v, %v), want (true, nil)", ok, err)
	}
	if remaining != -1 {
		t.Errorf("remaining = %d, want -1 for unlimited", remaining)
	}
	if l.Limit() != 0 {
		t.Errorf("Limit = %d, want 0", l.Limit())
	}
}

// TestKeyIsScopedToTheDay pins the reason the date is in the key rather than
// only in the TTL: a counter read a moment after midnight must not charge the
// previous day's usage to the new one.
func TestKeyIsScopedToTheDay(t *testing.T) {
	// Built directly rather than through New: New refuses a nil client, and the
	// key's shape is worth testing without a Redis server.
	l := &Limiter{prefix: "onexai", limit: 50}
	l.now = func() time.Time { return time.Date(2026, 10, 4, 23, 59, 0, 0, time.UTC) }
	late := l.key("go.kong")

	l.now = func() time.Time { return time.Date(2026, 10, 5, 0, 1, 0, 0, time.UTC) }
	early := l.key("go.kong")

	if late == early {
		t.Fatalf("the key did not change across the day boundary: %q", late)
	}
	if !strings.Contains(late, "20261004") || !strings.Contains(early, "20261005") {
		t.Errorf("keys do not name their day: %q / %q", late, early)
	}
}

func TestUntilTomorrow(t *testing.T) {
	l := &Limiter{prefix: "onexai", limit: 50}
	l.now = func() time.Time { return time.Date(2026, 10, 4, 22, 0, 0, 0, time.UTC) }
	if got := l.untilTomorrow(); got != 2*time.Hour {
		t.Errorf("untilTomorrow = %v, want 2h", got)
	}

	l.now = func() time.Time { return time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC) }
	if got := l.untilTomorrow(); got != 24*time.Hour {
		t.Errorf("untilTomorrow at midnight = %v, want 24h", got)
	}
}

func TestDescribe(t *testing.T) {
	if got := Describe(50); !strings.Contains(got, "50") {
		t.Errorf("Describe(50) = %q; it must name the quota that was reached", got)
	}
}
