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

package chatmodel

import (
	"strings"
)

// Capability names what a model endpoint can do beyond plain text.
//
// # Why a declared value rather than a guess
//
// Until this existed, the one question the code asked about a model — "can it
// see an image?" — was answered by looking at its name: anything that was not
// the DeepSeek family was assumed to have vision. That inference is a
// statement about the vendor rather than about the endpoint, and it is wrong
// in both directions. A self-hosted DeepSeek with a vision adapter is refused
// an image it could have read, and a proxy serving a text-only model under an
// OpenAI name is handed one it cannot.
//
// A model row that declares its capabilities is believed. A row that declares
// nothing falls back to the inference, so a deployment that has not thought
// about capabilities behaves exactly as it did before this field existed.
type Capability string

// # Why the list is short
//
// Only the capabilities something consults are declared. A name that changes
// no behaviour is a knob that lies: an operator sets it, nothing reads it, and
// the setting's only effect is to make the deployment look configured for
// something it is not.
//
// Two candidates are deliberately absent. "url-images" — whether an endpoint
// can fetch an http(s) image URL itself — was dropped because this service
// inlines every image as base64 at the model edge by design, so a passthrough
// would be unreachable. "reasoning" was dropped because reasoning is decided
// per request (the ask carries the flag) rather than inferred from the model,
// so declaring it would grant nothing.
const (
	// CapVision means the endpoint accepts image input.
	CapVision Capability = "vision"
	// CapImageGeneration means the endpoint serves an image-generation API.
	CapImageGeneration Capability = "image-generation"
	// CapTextOnly declares that the endpoint does NOT accept image input. It
	// exists because an empty declaration means "not declared" and falls back to
	// the vendor inference; a user-configured endpoint served under a
	// vision-looking name would otherwise be handed an image it rejects.
	CapTextOnly Capability = "text-only"
)

// Capabilities is a declared set.
type Capabilities map[Capability]bool

// Has reports whether the set contains a capability. A nil set has none.
func (c Capabilities) Has(cap Capability) bool { return c[cap] }

// Parse reads capability names into a set.
//
// Unknown names are kept rather than dropped. A row written by a newer version
// of this service and read by an older one names a capability the reader does
// not know; dropping it would make a round trip through the older version
// silently erase it. They simply answer false to every question the older
// version asks, which is the only answer it could act on anyway.
func Parse(names []string) Capabilities {
	if len(names) == 0 {
		return nil
	}
	out := make(Capabilities, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			out[Capability(name)] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Names returns the set's capability names, in a stable order.
//
// It exists so a round trip through this type does not reorder a row's
// declaration: a set has no order, and writing one back in map order would
// make the stored value differ from the configured one on every save.
func (c Capabilities) Names() []string {
	if len(c) == 0 {
		return nil
	}
	out := make([]string, 0, len(c))
	// The known capabilities first, in declaration order, so the common case
	// reads the way the constants do.
	for _, known := range []Capability{CapVision, CapImageGeneration, CapTextOnly} {
		if c[known] {
			out = append(out, string(known))
		}
	}
	// Then anything a newer version declared, sorted so the result is stable.
	var extra []string
	for name := range c {
		if !knownCapability(name) {
			extra = append(extra, string(name))
		}
	}
	sortStrings(extra)
	return append(out, extra...)
}

// knownCapability reports whether name is one this build defines.
func knownCapability(name Capability) bool {
	switch name {
	case CapVision, CapImageGeneration, CapTextOnly:
		return true
	default:
		return false
	}
}

// DefaultFor is the capability set an undeclared model is assumed to have.
//
// It reproduces the inference this service used before capabilities were
// declarable, so that adding the field changed nothing for a deployment that
// never fills it in. Non-DeepSeek endpoints are assumed to have vision, because
// that is what the OpenAI-compatible endpoints in use actually do; everything
// else is assumed absent, because assuming a capability is worse than not
// having it — a missing one costs an image, a wrong one costs the turn.
//
// DeepSeek is no longer one answer. Its v4 and vl generations accept image
// input; the older chat/reasoner names remain text-only. A row that declares
// "text-only" still wins in Resolve.
func DefaultFor(provider, modelName string) Capabilities {
	if IsDeepSeekFamily(provider, modelName) && !deepSeekSeesImages(modelName) {
		return Capabilities{}
	}
	return Capabilities{CapVision: true}
}

// deepSeekSeesImages reports whether a DeepSeek model name denotes a
// multimodal generation. It reads the upstream model name, not a deployment's
// logical endpoint name: a model row named "deepseek-secured" points at an
// upstream model, and that upstream name is what says whether images are
// accepted.
func deepSeekSeesImages(modelName string) bool {
	name := strings.ToLower(modelName)
	return strings.HasPrefix(name, "deepseek-v4") || strings.HasPrefix(name, "deepseek-vl")
}

// Resolve returns the set to use for a model: what it declared, or the default.
func Resolve(declared []string, provider, modelName string) Capabilities {
	if caps := Parse(declared); len(caps) > 0 {
		// An explicit text-only declaration beats both the vendor inference and
		// any other capability declared beside it: the conservative answer is
		// the one that cannot send a model something it rejects.
		if caps[CapTextOnly] {
			return Capabilities{}
		}
		return caps
	}
	return DefaultFor(provider, modelName)
}

// sortStrings is a small insertion sort, so this package needs no import for
// the one list it orders. The lists are a handful of names long.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
