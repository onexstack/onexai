// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package mcpserver

import (
	"context"
	"sync"

	"github.com/onexstack/onexai/internal/mcpserver/pkg/protocol"
)

// sseSessions maps a session ID to the channel of responses waiting to be
// pushed to that session's open /sse stream. It owns only session
// bookkeeping; the HTTP wiring around it lives in httpserver.go.
type sseSessions struct {
	mu  sync.Mutex
	chs map[string]chan *protocol.Response
}

func newSSESessions() *sseSessions {
	return &sseSessions{chs: make(map[string]chan *protocol.Response)}
}

// register creates a new session and returns the channel its /sse handler
// should read pending responses from. The channel is unbuffered: the sse
// transport's POST /messages?sessionId=... is synchronous by design (the
// server has fully processed the request by the time it responds), so a
// session never has more than one response in flight at a time.
func (s *sseSessions) register(id string) chan *protocol.Response {
	ch := make(chan *protocol.Response)

	s.mu.Lock()
	s.chs[id] = ch
	s.mu.Unlock()

	return ch
}

func (s *sseSessions) unregister(id string) {
	s.mu.Lock()
	delete(s.chs, id)
	s.mu.Unlock()
}

func (s *sseSessions) get(id string) (chan *protocol.Response, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch, ok := s.chs[id]
	return ch, ok
}

// deliver pushes resp to ch, blocking until the session's /sse reader picks
// it up or ctx (the delivering POST /messages request's context) is done.
// It reports whether the delivery succeeded, so callers can distinguish a
// genuine failure from a fire-and-forget success instead of silently
// dropping the response.
func deliver(ctx context.Context, ch chan<- *protocol.Response, resp *protocol.Response) bool {
	select {
	case ch <- resp:
		return true
	case <-ctx.Done():
		return false
	}
}
