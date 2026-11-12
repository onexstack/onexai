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

// Package lark provides a typed client for the Lark (Feishu) SDK. It exposes the
// underlying SDK client rather than wrapping its extensive IM/Cardkit/Bitable
// method surface.
package lark

import (
	larkv3 "github.com/larksuite/oapi-sdk-go/v3"
)

// Interface defines the operations for the Lark (Feishu) SDK client.
type Interface interface {
	// C returns the underlying Lark SDK client.
	C() *larkv3.Client
}

// Client implements Interface by wrapping a Lark SDK client.
type Client struct {
	client *larkv3.Client
}

var _ Interface = (*Client)(nil)

// NewForConfig creates a Client configured with the provided app credentials.
func NewForConfig(appID, appSecret string) *Client {
	return &Client{client: larkv3.NewClient(appID, appSecret)}
}

// C returns the underlying Lark SDK client.
func (c *Client) C() *larkv3.Client {
	return c.client
}
