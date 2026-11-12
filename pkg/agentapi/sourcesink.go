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

import "context"

// SourceRef names one document a retrieval handed the model.
//
// # Why a run has to report these at all
//
// A tool returns a string, and the model reads it. What that string does not
// say is *which documents* the passages in it came from, in a form a caller can
// act on: a caller that wants to show a reader where an answer came from needs
// the identity of the material, not a sentence about it. Neither is asking the
// model to name them — a model that paraphrases is doing its job, and one that
// invents a name is doing what it was asked.
//
// So the tool reports the documents out of band, as references, and the caller
// decides what a person sees.
type SourceRef struct {
	// DocumentKey is the corpus's own identifier for the document — a name, an
	// id, whatever the platform holding it calls it.
	//
	// It is opaque here on purpose: this service neither interprets it nor
	// builds any address out of it. Turning it into something a person can open
	// is the job of whoever owns the corpus, because only they know the address
	// space, the permission model, and whether the material is still published.
	DocumentKey string
	// Title is the document's human-readable name, when the platform reports
	// one. Empty means the platform does not know one — a corpus that names its
	// documents by id and keeps the title inside the text is a real case, not a
	// failure.
	Title string
	// URL is the platform's own address for the document, when it reports one.
	// It is never built here: a missing address stays missing rather than
	// becoming a guess.
	URL string
}

// SourceSink receives the documents a retrieval returned during a run.
//
// # Why the run's context and not the engine's
//
// The same reason ImageSink gives: the sink belongs to one run, it writes to
// that run's event channel in that run's order, and it stops working when the
// run does. A sink on the engine would be shared by every conversation in
// flight, which would deliver one conversation's sources to whoever is
// listening.
type SourceSink interface {
	// Sources reports the documents behind the passages a tool returned. It is
	// called from the goroutine running the tool, so an implementation must be
	// safe to call concurrently with the run's own event emission.
	Sources(ctx context.Context, refs []SourceRef)
}

// sourceSinkKey is the context key. It is a private struct so no other package
// can install a value under it by accident.
type sourceSinkKey struct{}

// WithSourceSink attaches a sink to a context.
func WithSourceSink(ctx context.Context, s SourceSink) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, sourceSinkKey{}, s)
}

// SourceSinkFrom returns the sink attached to a context, or nil.
//
// A nil answer is the ordinary case — an agent with no retrieval tool, or a
// caller that renders no sources — and every caller treats it as "nobody wants
// them" rather than as an error: the tool still retrieves, and what it read
// still reaches the model.
func SourceSinkFrom(ctx context.Context) SourceSink {
	s, _ := ctx.Value(sourceSinkKey{}).(SourceSink)
	return s
}
