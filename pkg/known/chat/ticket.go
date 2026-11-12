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

package chat

// Ticket chat naming and status constants used by the duty bot.
const (
	// [Status][Level]Title, for example: [Processing][P4]EKS issue consultation
	ChatNameFormat = "[%s][%s]%s"

	StatusProcessing = "处理中" // Processing
	StatusClosed     = "已关闭" // Closed
	StatusSaved      = "已保存" // Closed
)

const LastNMessage = 100

// ObserverGroupID is the only duty group whose ticket chats invite the
// configured observers. Tickets for other modules skip them.
const ObserverGroupID = "eks_ec2"
