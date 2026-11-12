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

// Package dnswhitelist holds an in-memory DNS whitelist refreshed from a remote
// dnsmasq forward.conf file.
package dnswhitelist

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/astore"
)

// snapshot is one generation of the parsed forward.conf. The sets are never
// mutated after being published.
type snapshot struct {
	exact  map[string]struct{}
	suffix map[string]struct{}
}

// Whitelist holds an in-memory DNS whitelist refreshed periodically from a
// remote dnsmasq forward.conf file.
//
// The list is a security boundary, so New fails when the first load does: a
// process that cannot read its whitelist has nothing it may allow. Later
// refreshes keep the previous generation on failure, which keeps an outage from
// turning an allow into a deny.
type Whitelist struct {
	cache      *astore.Store[snapshot]
	rawURL     string
	token      string
	httpClient *http.Client
}

// CheckResult is returned by Check.
type CheckResult struct {
	Domain      string `json:"domain"`
	InWhitelist bool   `json:"in_whitelist"`
	MatchedRule string `json:"matched_rule,omitempty"`
}

// New creates a Whitelist and immediately loads the remote file.
// It also starts a background goroutine that refreshes the list every
// refreshInterval (a non-positive interval means no background refresh).
// token is the already-decrypted GitLab PRIVATE-TOKEN; pass empty string for public repos.
func New(ctx context.Context, rawURL, token string, refreshInterval time.Duration) (*Whitelist, error) {
	w := &Whitelist{
		rawURL:     rawURL,
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	w.cache = astore.New(astore.Options{
		Name:     "dnswhitelist",
		Interval: refreshInterval,
	}, w.fetch)

	if err := w.cache.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("dnswhitelist: initial load failed: %w", err)
	}

	// A generation is published, so Start only schedules the refresher.
	if err := w.cache.Start(ctx); err != nil {
		return nil, fmt.Errorf("dnswhitelist: %w", err)
	}
	return w, nil
}

// Check reports whether domain is covered by the whitelist.
func (w *Whitelist) Check(domain string) CheckResult {
	normalized := strings.ToLower(strings.TrimSuffix(domain, "."))

	gen, ok := w.cache.Current()
	if !ok {
		return CheckResult{Domain: normalized, InWhitelist: false}
	}

	if _, ok := gen.Value.exact[normalized]; ok {
		return CheckResult{Domain: normalized, InWhitelist: true, MatchedRule: normalized}
	}

	cur := normalized
	for {
		if _, ok := gen.Value.suffix[cur]; ok {
			return CheckResult{Domain: normalized, InWhitelist: true, MatchedRule: "." + cur}
		}
		idx := strings.Index(cur, ".")
		if idx < 0 {
			break
		}
		cur = cur[idx+1:]
	}

	return CheckResult{Domain: normalized, InWhitelist: false}
}

// LastUpdated returns when the whitelist was last successfully refreshed.
func (w *Whitelist) LastUpdated() time.Time {
	loadedAt, _ := w.cache.Status()
	return loadedAt
}

// fetch reads and parses the remote forward.conf into one generation.
func (w *Whitelist) fetch(ctx context.Context) (snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.rawURL, nil)
	if err != nil {
		return snapshot{}, err
	}
	if w.token != "" {
		req.Header.Set("PRIVATE-TOKEN", w.token)
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return snapshot{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return snapshot{}, fmt.Errorf("unexpected HTTP status %d fetching forward.conf", resp.StatusCode)
	}

	exact, suffix, parseErr := parse(resp.Body)
	if parseErr != nil {
		return snapshot{}, fmt.Errorf("parse forward.conf: %w", parseErr)
	}

	slog.InfoContext(ctx, "dnswhitelist: refreshed", "exact_count", len(exact), "suffix_count", len(suffix))
	return snapshot{exact: exact, suffix: suffix}, nil
}

// parse reads a dnsmasq forward.conf and returns two domain sets.
func parse(r io.Reader) (exact map[string]struct{}, suffix map[string]struct{}, err error) {
	exact = make(map[string]struct{})
	suffix = make(map[string]struct{})

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "server=/") {
			continue
		}
		rest := strings.TrimPrefix(line, "server=/")
		domain, _, ok := strings.Cut(rest, "/")
		if !ok || domain == "" {
			continue
		}
		domain = strings.ToLower(domain)
		if after, found := strings.CutPrefix(domain, "."); found {
			suffix[after] = struct{}{}
		} else {
			exact[domain] = struct{}{}
		}
	}
	return exact, suffix, scanner.Err()
}
