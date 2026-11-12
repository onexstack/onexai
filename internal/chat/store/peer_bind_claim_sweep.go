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

package store

import (
	"context"
	"log/slog"
	"time"
)

// PeerBindClaimSweepInterval is how often expired binding claims are dropped.
//
// A minute is chosen the way the ticket claim sweep in biz/v1/bot chooses its
// own: the table only grows when somebody speaks a code, and it is bounded by
// how many people bind in a day, so the sweep exists to keep it from growing
// without limit rather than to be timely. One indexed DELETE a minute on a
// table this small is not worth tuning.
const PeerBindClaimSweepInterval = time.Minute

// RunPeerBindClaimSweeper deletes expired claims until ctx is done.
//
// # Why this has to exist at all
//
// Nothing consumes a claim on read: the business side reads one and leaves it,
// because the row is only evidence of who spoke a code (see CleanupExpired).
// Every binding therefore leaves its row behind, and without this loop the
// table grows for the life of the deployment.
//
// It is a function rather than a method because the caller is the server's
// lifecycle — the same place session reaping is started — and it has no
// business holding a store of its own.
func RunPeerBindClaimSweeper(ctx context.Context, claims PeerBindClaimStore, interval time.Duration) {
	if claims == nil || interval <= 0 {
		// Nothing to sweep, or no schedule to sweep on. Returning is better
		// than guessing an interval nobody chose.
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := claims.CleanupExpired(ctx)
			if err != nil {
				// A failed sweep is not fatal: the rows stay, and the next tick
				// tries again.
				slog.WarnContext(ctx, "peer bind claim sweep failed", "error", err)
				continue
			}
			if n > 0 {
				// Only when something went, so a steady state does not log.
				slog.DebugContext(ctx, "swept expired peer bind claims", "count", n)
			}
		}
	}
}
