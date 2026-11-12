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
	"sync"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/model"
)

// countingClaims is a PeerBindClaimStore that only counts sweeps. What the
// delete does is covered against a real database by
// TestPeerBindClaimCleanupExpiredSweepsOnlyLapsedRows; what this file has to
// prove is that anything calls it at all.
type countingClaims struct {
	PeerBindClaimStore
	mu     sync.Mutex
	sweeps int
}

func (c *countingClaims) Claim(context.Context, string, string, string, string, time.Duration) (bool, string, error) {
	return false, "", nil
}

func (c *countingClaims) Reissue(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}

func (c *countingClaims) GetLive(context.Context, string) (*model.PeerBindClaimM, error) {
	return nil, nil
}

func (c *countingClaims) CleanupExpired(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweeps++
	return 0, nil
}

func (c *countingClaims) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sweeps
}

func TestRunPeerBindClaimSweeperSweepsUntilCancelled(t *testing.T) {
	claims := &countingClaims{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		RunPeerBindClaimSweeper(ctx, claims, 5*time.Millisecond)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for claims.count() == 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("the sweeper never ran a sweep")
		case <-time.After(2 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweeper did not return when its context was cancelled")
	}
}

// A nil store or a non-positive interval is a deployment that has nothing to
// sweep or no schedule to sweep on; returning is better than guessing one.
func TestRunPeerBindClaimSweeperWithoutWorkReturns(t *testing.T) {
	done := make(chan struct{})
	go func() {
		RunPeerBindClaimSweeper(context.Background(), nil, time.Minute)
		RunPeerBindClaimSweeper(context.Background(), &countingClaims{}, 0)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweeper blocked with nothing to do")
	}
}
