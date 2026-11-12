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

package v1

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// The generated *_Default methods cover static defaults. These request types
// also need a value produced at bind time (a timestamp, a UUID, or a fallback
// from another field), which a proto annotation cannot express. Their messages
// are marked (defaults.unexported), so this file is the only exported Default
// implementation for each of them.
//
// core.ReadRequest calls Default after binding and before validation, so a
// validator sees these values already filled in.

// Default fills the conversation id for the OpenAI-compatible completion
// endpoint.
func (x *ChatCompletionRequest) Default() {
	if x == nil {
		return
	}

	x._Default()

	if x.GetSessionID() == "" {
		x.SessionID = fmt.Sprintf("thread_%d", time.Now().UnixNano()) //nolint:protogetter // writing the request field
	}
}

// Default mints the run id for the backend-neutral ask endpoint.
func (x *AgentAskRequest) Default() {
	if x == nil {
		return
	}

	x._Default()

	if x.GetRunId() == "" {
		x.RunId = uuid.NewString() //nolint:protogetter // writing the request field
	}
}

// Default mints the correlation id for a custom-command invocation.
func (x *InvokeCustomCommandRequest) Default() {
	if x == nil {
		return
	}

	x._Default()

	if x.GetUid() == "" {
		x.Uid = uuid.NewString() //nolint:protogetter // writing the request field
	}
}

// Default fills the inline model and its name for the connectivity test.
func (x *TestModelRequest) Default() {
	if x == nil {
		return
	}

	x._Default()

	if x.GetModel() == nil {
		x.Model = new(Model) //nolint:protogetter,exhaustruct // zero value is the intended default
	}

	if x.GetName() == "" {
		x.Name = x.GetModel().GetName() //nolint:protogetter // writing the request field
	}
}
