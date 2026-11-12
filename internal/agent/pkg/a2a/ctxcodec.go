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

package a2a

import (
	"context"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

// Keys used in the encoded context. They are part of the wire format between
// replicas of the same build, not a persisted schema, so renaming one is a
// rollout concern only.
const (
	codecUserKey       = "user"
	codecNameKey       = "name"
	codecAuthedKey     = "authenticated"
	codecSubjectKey    = "subject"
	codecKindKey       = "kind"
	codecIDKey         = "id"
	codecMaxTokensKey  = "maxOutputTokens"
	codecAttributesKey = "attributes"
)

// CallContextCodec carries a run's caller and settings to whichever replica
// executes it.
//
// In cluster mode the request is answered by one replica and executed by
// possibly another, so everything the edge decided before enqueueing has to
// travel with the work item or the run silently loses it:
//
//   - the authenticated caller, which the run is authorized, metered and
//     recorded as (and which the task store's ownership check reads);
//   - the authorization subject, so tool-level authorization inside the run
//     still sees the same principal the request did;
//   - the per-caller output ceiling, which is attached to the request context at
//     the edge precisely because no later layer can re-derive it.
//
// Without this the executing replica starts from a bare context: the run would
// be recorded as the surface itself, and a task it creates would belong to
// nobody — or, worse, to the empty owner, which the store treats as a real one.
type CallContextCodec struct{}

var _ a2asrv.ContextCodec = CallContextCodec{}

// Encode implements a2asrv.ContextCodec.
func (CallContextCodec) Encode(ctx context.Context) (map[string]any, error) {
	encoded := map[string]any{}

	if callCtx, ok := a2asrv.CallContextFrom(ctx); ok && callCtx.User != nil && callCtx.User.Name != "" {
		user := map[string]any{
			codecNameKey:   callCtx.User.Name,
			codecAuthedKey: callCtx.User.Authenticated,
		}
		// The attributes are what say *what* the caller is; without them the
		// executing replica would record a service account as a user.
		if len(callCtx.User.Attributes) > 0 {
			user[codecAttributesKey] = callCtx.User.Attributes
		}
		encoded[codecUserKey] = user
	}

	if subject, ok := authz.SubjectFromContext(ctx); ok {
		if id := subject.PrimaryID(); id != "" {
			encoded[codecSubjectKey] = map[string]any{
				codecKindKey: string(subject.Kind),
				codecIDKey:   id,
				codecNameKey: subject.UserName,
			}
		}
	}

	if ceiling := chatmodel.MaxOutputTokens(ctx); ceiling > 0 {
		encoded[codecMaxTokensKey] = ceiling
	}

	if len(encoded) == 0 {
		return nil, nil
	}
	return encoded, nil
}

// Decode implements a2asrv.ContextCodec.
func (CallContextCodec) Decode(ctx context.Context, data map[string]any) (context.Context, error) {
	if data == nil {
		return ctx, nil
	}

	if name := nestedString(data, codecUserKey, codecNameKey); name != "" {
		authenticated, _ := nestedBool(data, codecUserKey, codecAuthedKey)
		var callCtx *a2asrv.CallContext
		ctx, callCtx = a2asrv.NewCallContext(ctx, a2asrv.NewServiceParams(nil))
		callCtx.User = &a2asrv.User{Name: name, Authenticated: authenticated}
		if nested, ok := data[codecUserKey].(map[string]any); ok {
			if attributes, ok := nested[codecAttributesKey].(map[string]any); ok {
				callCtx.User.Attributes = attributes
			}
		}
	}

	if subject, ok := data[codecSubjectKey].(map[string]any); ok {
		if encoded := decodeSubject(subject); encoded != nil {
			ctx = authz.ContextWithSubject(ctx, *encoded)
		}
	}

	if ceiling, ok := number(data[codecMaxTokensKey]); ok && ceiling > 0 {
		ctx = chatmodel.WithMaxOutputTokens(ctx, ceiling)
	}

	return ctx, nil
}

// decodeSubject rebuilds the authorization subject from its encoded form.
func decodeSubject(encoded map[string]any) *authz.Subject {
	id, _ := encoded[codecIDKey].(string)
	kind, _ := encoded[codecKindKey].(string)
	if id == "" || kind == "" {
		return nil
	}

	name, _ := encoded[codecNameKey].(string)
	subject := authz.Subject{Kind: authz.SubjectKind(kind), UserName: name}
	switch subject.Kind {
	case authz.SubjectKindServiceAccount:
		subject.ServiceName = id
	case authz.SubjectKindGroup:
		subject.GroupCode = id
	default:
		subject.UserID = id
	}
	return &subject
}

// nestedString reads data[outer][inner] as a string.
func nestedString(data map[string]any, outer, inner string) string {
	nested, ok := data[outer].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := nested[inner].(string)
	return value
}

// nestedBool reads data[outer][inner] as a bool.
func nestedBool(data map[string]any, outer, inner string) (bool, bool) {
	nested, ok := data[outer].(map[string]any)
	if !ok {
		return false, false
	}
	value, ok := nested[inner].(bool)
	return value, ok
}

// number reads a JSON-roundtripped number. The value travels through JSON in the
// work item, so an int becomes a float64 on the other side.
func number(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	}
	return 0, false
}
