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

	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// CommandCatalog is the read-only view of the chat command catalogue.
//
// It is deliberately not part of AIStore: the catalogue is owned by the chat
// service and served through a different API group, so a store that has no
// control-plane client simply does not implement it. Callers that need it — the
// binding preview, which expands a commandSelector against the same labels the
// runtime uses — type-assert for it and fall back to the named commands.
type CommandCatalog interface {
	// ListCommands returns one entry per catalogued command. The catalogue is
	// published into every namespace a binding resolves from, so the same name
	// appears more than once; implementations deduplicate it.
	ListCommands(ctx context.Context) ([]*chatv1alpha1.Command, error)
}
