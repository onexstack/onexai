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

package escalation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/lark"
)

// LarkNotifier sends plain-text direct messages through the existing Lark bot.
type LarkNotifier struct {
	client lark.Interface
}

var _ Notifier = (*LarkNotifier)(nil)

// NewLarkNotifier wraps the chat service's Lark client.
func NewLarkNotifier(client lark.Interface) *LarkNotifier {
	return &LarkNotifier{client: client}
}

// Notify sends one text message to userID. The identity is the tenant-scoped
// Lark user_id, which is what the on-call name map contains.
func (n *LarkNotifier) Notify(ctx context.Context, userID, userName, subject, body string) error {
	if n == nil || n.client == nil || n.client.C() == nil {
		return errors.New("lark client is not configured")
	}
	if userID == "" {
		return fmt.Errorf("no lark user_id for %s", userName)
	}

	content, err := json.Marshal(map[string]string{"text": subject + "\n\n" + body})
	if err != nil {
		return err
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("user_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(userID).
			MsgType("text").
			Content(string(content)).
			Build()).
		Build()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := n.client.C().Im.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("lark send failed, code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}
