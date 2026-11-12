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

// Package settingstore serves the deployment's runtime settings from a
// ConfigMap in the control plane.
//
// The distinction from configuration is the point: a model endpoint, a port, a
// feature switch are deployment facts and belong in a file that a restart
// applies. A system prompt is an editorial decision an operator makes between
// releases, and requiring a redeploy for it means it never gets tuned.
//
// The settings live in a ConfigMap rather than in a table of their own. It is
// the object k8s keeps process-wide, reloadable settings in (CoreDNS's Corefile,
// kube-proxy's config), `kubectl edit configmap` is the write path, and the
// informer watch makes an edit visible to the next lookup without a poll. An
// absent ConfigMap and an absent key both mean "unset", which reads as the empty
// string.
package settingstore

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	corelisters "k8s.io/client-go/listers/core/v1"
)

// KeySystemPrompt is the global system prompt prepended to every agent.
const KeySystemPrompt = "sys_prompt"

// Source resolves one setting key.
//
// It is deliberately narrower than a store: this package never writes, and a
// source that can only answer a lookup can be a ConfigMap lister, a CR lister or
// a test literal without any of them knowing about the others.
type Source interface {
	// Lookup returns the value of key and whether the key is set at all.
	Lookup(key string) (string, bool)
}

// NewConfigMapSource reads the settings from the Data of one ConfigMap.
//
// The lister is an informer cache, so a lookup is a map read and a change is
// visible as soon as the watch delivers it. configMaps is narrowed to the
// control plane's namespace; name is the ConfigMap the deployment declared.
func NewConfigMapSource(configMaps corelisters.ConfigMapNamespaceLister, name string) Source {
	return &configMapSource{configMaps: configMaps, name: name}
}

type configMapSource struct {
	configMaps corelisters.ConfigMapNamespaceLister
	name       string
}

// Lookup reads one key out of the ConfigMap.
func (s *configMapSource) Lookup(key string) (string, bool) {
	if s == nil || s.configMaps == nil || s.name == "" || key == "" {
		return "", false
	}
	configMap, err := s.configMaps.Get(s.name)
	if err != nil || configMap == nil {
		// A missing ConfigMap is a deployment that declared no settings, not a
		// failure worth logging: every caller falls back to its own default, and
		// a warning per lookup would be noise about a deliberate state.
		return "", false
	}
	value, ok := configMap.Data[key]
	return value, ok
}

// Store answers setting lookups from the source.
//
// Every method is safe on a nil receiver and answers "unset": a runtime whose
// settings were not wired is a deployment with no overrides, and reading the
// fallback is the only correct answer there.
type Store struct {
	source Source
}

// New builds a Store over source, or nil when there is no source — nil being how
// a deployment says "this service has no runtime settings".
func New(source Source) *Store {
	if source == nil {
		return nil
	}
	return &Store{source: source}
}

// GetString returns the setting's value, or the empty string when it is unset.
func (s *Store) GetString(key string) string {
	if s == nil || s.source == nil {
		return ""
	}
	value, _ := s.source.Lookup(key)
	return value
}

// GetInt returns the setting as an integer, or fallback when it is unset or not
// a number. A malformed value falling back rather than erroring is deliberate:
// nothing here is worth failing a request over.
func (s *Store) GetInt(key string, fallback int) int {
	raw := strings.TrimSpace(s.GetString(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("settingstore: setting is not an integer; using the fallback", "key", key, "fallback", fallback)
		return fallback
	}
	return v
}

// GetBool returns the setting as a boolean, or fallback when it is unset or not
// a boolean.
func (s *Store) GetBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(s.GetString(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		slog.Warn("settingstore: setting is not a boolean; using the fallback", "key", key, "fallback", fallback)
		return fallback
	}
	return v
}

// GetDuration returns the setting as a duration, or fallback when it is unset
// or not a duration.
func (s *Store) GetDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(s.GetString(key))
	if raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("settingstore: setting is not a duration; using the fallback", "key", key, "fallback", fallback)
		return fallback
	}
	return v
}

// GetStringSlice returns the setting split on commas, trimming each entry and
// dropping blanks. An unset setting yields nil rather than an empty slice, so
// "unset" and "set to nothing" are the same thing to a caller.
func (s *Store) GetStringSlice(key string) []string {
	raw := s.GetString(key)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
