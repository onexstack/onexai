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

package agentapi

import (
	"context"

	"github.com/onexstack/onexai/pkg/media"
)

// ImageSink receives images a tool produced during a run.
//
// # Why a tool needs one
//
// A tool returns a string, and nothing else: the model reads that string and
// the run goes on. A drawn picture is not something to put in that string — the
// bytes would be a base64 payload the model is billed to read, and the answer
// it produces from them is not what the person who asked for a picture wants.
//
// So the tool reports the picture out of band and tells the model, in words,
// that it drew one. This interface is the out-of-band channel, carried on the
// run's context so a tool reaches it without the tool set having to be
// reassembled per run.
//
// # Why the run's context and not the engine's
//
// The sink belongs to one run: it writes to that run's event channel, in that
// run's order, and it stops working when the run does. A sink on the engine
// would be shared by every conversation in flight, which is a picture delivered
// to whoever is listening rather than to whoever asked.
type ImageSink interface {
	// Image reports one or more generated images. It is called from the
	// goroutine running a tool, so an implementation must be safe to call
	// concurrently with the run's own event emission.
	Image(ctx context.Context, refs []media.Ref)
}

// imageSinkKey is the context key. It is a private struct so no other package
// can install a value under it by accident.
type imageSinkKey struct{}

// WithImageSink attaches a sink to a context.
func WithImageSink(ctx context.Context, s ImageSink) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, imageSinkKey{}, s)
}

// ImageSinkFrom returns the sink attached to a context, or nil.
//
// A nil answer is the ordinary case for a run that is not drawing anything, and
// every caller treats it as "nobody wants pictures" rather than as an error:
// the tool still draws, and its words still reach the model.
func ImageSinkFrom(ctx context.Context) ImageSink {
	s, _ := ctx.Value(imageSinkKey{}).(ImageSink)
	return s
}
