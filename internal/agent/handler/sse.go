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

package handler

import (
	"github.com/gin-gonic/gin"

	mw "github.com/onexstack/onexai/internal/pkg/middleware/gin"
)

// openSSE commits the response to an event stream and lifts the connection's
// absolute deadline, which would otherwise cut a run off mid-stream.
//
// It is one helper rather than three lines at each streaming handler: the
// header set and the deadline lift have to agree, and a surface that forgot the
// second one would truncate exactly the long runs SSE exists to carry. The
// returned error is the deadline lift's, and it is advisory — the response is
// already committed by then, so a caller can only log it.
func openSSE(c *gin.Context) error {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	return detachConnDeadlines(mw.ConnWriter(c))
}
