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
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"sort"
	"strings"
)

// NormalizeNames trims, drops blank entries and removes duplicates while
// preserving first-seen order.
//
// Composition names come from a browser and reach the resource lookups
// directly. Normalizing here means every consumer — validation, authorization,
// the runner, the digest — sees the same list, and a request that names the
// same skill twice does not get two entries in a capability token.
func NormalizeNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeSorted is NormalizeNames plus a stable sort, for the capability
// kinds whose order carries no meaning.
func normalizeSorted(names []string) []string {
	out := NormalizeNames(names)
	sort.Strings(out)
	return out
}

// Normalized returns a copy with every list trimmed and deduplicated. Prompt
// order is preserved because it decides how the instruction is concatenated;
// the capability kinds are sorted because two callers naming the same set in a
// different order describe the same composition.
func (c *AgentComposition) Normalized() *AgentComposition {
	if c == nil {
		return nil
	}
	return &AgentComposition{
		Prompts:        NormalizeNames(c.Prompts),
		Skills:         normalizeSorted(c.Skills),
		MCPs:           normalizeSorted(c.MCPs),
		KnowledgeBases: normalizeSorted(c.KnowledgeBases),
		CustomCommands: normalizeSorted(c.CustomCommands),
		Model:          strings.TrimSpace(c.Model),
		Namespace:      strings.TrimSpace(c.Namespace),
	}
}

// Empty reports whether the composition selects nothing at all.
//
// It is not a validity question: a run with no prompt, no tools and no model
// override is a legitimate way to see what the host identity alone does.
func (c *AgentComposition) Empty() bool {
	if c == nil {
		return true
	}
	return len(c.Prompts) == 0 &&
		len(c.Skills) == 0 &&
		len(c.MCPs) == 0 &&
		len(c.KnowledgeBases) == 0 &&
		len(c.CustomCommands) == 0 &&
		strings.TrimSpace(c.Model) == ""
}

// CanonicalDigest returns a stable digest of the composition.
//
// It is what the debug session bucket is derived from. Prompt order is part of
// the digest because it changes the instruction; the capability kinds are
// sorted so that naming the same set in a different order keeps the same
// conversation.
func (c *AgentComposition) CanonicalDigest() string {
	h := sha256.New()
	write := func(s string) {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(s)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(s))
	}
	writeList := func(label string, names []string) {
		write(label)
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(names)))
		_, _ = h.Write(size[:])
		for _, name := range names {
			write(name)
		}
	}

	normalized := c.Normalized()
	if normalized == nil {
		write("empty")
	} else {
		writeList("prompts", normalized.Prompts)
		writeList("skills", normalized.Skills)
		writeList("mcps", normalized.MCPs)
		writeList("knowledgeBases", normalized.KnowledgeBases)
		writeList("customCommands", normalized.CustomCommands)
		write("model")
		write(normalized.Model)
		// The referrer namespace decides how bare names resolve and which
		// grants apply, so the same list in two business lines is not the same
		// conversation.
		write("namespace")
		write(normalized.Namespace)
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// HostlessAgentLabel is the reserved bucket label for a composed run with no
// host agent.
//
// It is deliberately not an agent name: a real agent cannot be created with
// this spelling (the console's name pattern forbids leading underscores), and
// it exists only inside the session digest. The engine still sees an empty
// selection — writing the label into Selection.Agents would make Approve try to
// load it as a real agent.
const HostlessAgentLabel = "__playground__"

// ComposeSessionID derives the memory bucket of one composed debug run.
//
// # Why it is a digest rather than a readable tuple
//
// The session key is persisted in columns declared size:128. A readable key
// naming the agent, subject, actor, every component and a nonce would overflow
// as soon as an agent or a resource name was long, and the failure would be a
// database write error in the middle of a run. The prefix stays readable
// because that is what makes a composed bucket distinguishable from the normal
// `usr:`/`dbg:` buckets; everything after it is opaque.
//
// An empty agent means a hostless playground run. It is replaced by the
// reserved HostlessAgentLabel before hashing so the bucket is still a stable,
// well-formed identity rather than an empty component.
func ComposeSessionID(prefix, agent, subjectID, actorID string, c *AgentComposition, nonce string) string {
	if agent == "" {
		agent = HostlessAgentLabel
	}
	h := sha256.New()
	write := func(s string) {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(s)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(s))
	}
	write(agent)
	write(subjectID)
	write(actorID)
	write(c.CanonicalDigest())
	write(nonce)
	token := base64.RawURLEncoding.EncodeToString(h.Sum(nil))[:24]
	return prefix + ":compose:" + token
}
