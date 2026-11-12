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
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"

	"github.com/onexstack/onexstack/pkg/server"

	"github.com/onexstack/onexai/internal/mcpserver/pkg/protocol"
	_ "github.com/onexstack/onexai/internal/mcpserver/tool/all"
)

// stdioServer implements the server.Server interface over stdin/stdout,
// reading newline-delimited JSON-RPC requests and writing newline-delimited
// JSON-RPC responses
type stdioServer struct {
	handler RequestHandler

	in  io.Reader
	out io.Writer

	mu   sync.Mutex // serializes writes to out
	done chan struct{}
}

// Ensure *stdioServer implements the server.Server interface.
var _ server.Server = (*stdioServer)(nil)

// RequestHandler is the subset of *handler.Handler required by the
// stdio transport, kept as an interface so the transport can be tested
// without constructing a full Handler.
type RequestHandler interface {
	Handle(ctx context.Context, req *protocol.Request) *protocol.Response
}

// NewStdioServer creates a new stdio-transport MCP server that dispatches
// requests to h, reading from in and writing to out.
func NewStdioServer(h RequestHandler, in io.Reader, out io.Writer) *stdioServer {
	return &stdioServer{
		handler: h,
		in:      in,
		out:     out,
		done:    make(chan struct{}),
	}
}

// Run starts reading requests from stdin until ctx is canceled or the
// input stream is closed (EOF). It never panics on malformed input: parse
// errors are reported back as JSON-RPC errors, per the specification.
func (s *stdioServer) Run(ctx context.Context) error {
	defer close(s.done)

	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	slog.InfoContext(ctx, "start to listening the incoming requests", "transport", "stdio")

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		s.handleLine(ctx, append([]byte(nil), line...))
	}

	if err := scanner.Err(); err != nil {
		slog.ErrorContext(ctx, "stdio scanner stopped with error", "error", err)
		return err
	}

	return nil
}

func (s *stdioServer) handleLine(ctx context.Context, line []byte) {
	var req protocol.Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.write(protocol.NewErrorResponse(nil, protocol.CodeParseError, err.Error(), nil))
		return
	}

	resp := s.handler.Handle(ctx, &req)
	if resp == nil {
		// Notifications (and unknown-method notifications) get no response.
		return
	}

	s.write(resp)
}

func (s *stdioServer) write(resp *protocol.Response) {
	b, err := protocol.Encode(resp)
	if err != nil {
		slog.Error("failed to marshal mcp response", "error", err)
		return
	}
	b = append(b, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.out.Write(b); err != nil {
		slog.Error("failed to write mcp response", "error", err)
	}
}

// GracefulStop waits for the current read loop to finish, or for ctx to
// expire, whichever happens first.
func (s *stdioServer) GracefulStop(ctx context.Context) error {
	select {
	case <-s.done:
	case <-ctx.Done():
	}
	return nil
}
