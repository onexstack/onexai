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

// Package protocol defines the JSON-RPC 2.0 envelope and MCP-specific
// message payloads used by oai-mcpserver's stdio transport.
package protocol

import "encoding/json"

// JSONRPCVersion is the JSON-RPC protocol version required by the MCP spec.
const JSONRPCVersion = "2.0"

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Request represents an incoming JSON-RPC 2.0 request or notification.
// A notification is a Request whose ID is nil/empty and MUST NOT receive a Response.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether the request carries no ID and therefore
// expects no response, per the JSON-RPC 2.0 specification.
func (r *Request) IsNotification() bool {
	return len(r.ID) == 0
}

// Response represents an outgoing JSON-RPC 2.0 response.
// Result and Error are mutually exclusive.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error represents a JSON-RPC 2.0 error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// NewResultResponse builds a successful JSON-RPC response for the given request ID.
func NewResultResponse(id json.RawMessage, result any) *Response {
	return &Response{JSONRPC: JSONRPCVersion, ID: id, Result: result}
}

// NewErrorResponse builds a failed JSON-RPC response for the given request ID.
func NewErrorResponse(id json.RawMessage, code int, message string, data any) *Response {
	return &Response{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Error:   &Error{Code: code, Message: message, Data: data},
	}
}

// Encode marshals resp to JSON. Every transport needs this same
// marshal-or-log-and-report-nothing-useful step, so it lives here once
// instead of being duplicated per transport.
func Encode(resp *Response) ([]byte, error) {
	return json.Marshal(resp)
}
