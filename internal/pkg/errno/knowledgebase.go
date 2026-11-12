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

package errno

import (
	"net/http"

	"github.com/onexstack/onexstack/pkg/errorsx"
)

// ErrKnowledgeBaseRetrievalUnsupported refuses a retrieval test against a
// record whose platform cannot answer it.
//
// It is a 400, not a 503: the request is well-formed but does not apply to this
// record. A deployment that does support the platform would answer the same
// request for a different knowledge base.
var ErrKnowledgeBaseRetrievalUnsupported = errorsx.New(
	http.StatusBadRequest,
	"InvalidArgument.KnowledgeBaseRetrievalUnsupported",
	"Retrieval tests are only supported for RAGFlow knowledge bases.",
)
