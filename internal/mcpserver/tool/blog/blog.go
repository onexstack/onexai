// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// Package blog registers the "blog" namespace: read-only tools over
// miniblog post data. It ships with a small in-memory seed dataset so the
// namespace is runnable standalone in examples and tests without requiring
// a live database connection; swap Store for one backed by
// internal/apiserver/store to expose real post data.
package blog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Namespace is the tool namespace this package registers under.
const Namespace = "blog"

// Post is a minimal read-only projection of a miniblog post.
type Post struct {
	PostID string `json:"postID"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// Store provides read-only access to posts. The default implementation is
// an in-memory seed dataset; see NewStore.
type Store struct {
	mu    sync.RWMutex
	posts map[string]Post
}

// defaultStore is the process-wide store used by the registered tools.
var defaultStore = NewStore([]Post{
	{PostID: "post-1", Title: "Hello, miniblog", Body: "This is the first post on miniblog."},
	{PostID: "post-2", Title: "Understanding MCP", Body: "MCP tools can be grouped into namespaces, similar to Kubernetes."},
	{PostID: "post-3", Title: "Go generics 101", Body: "A short introduction to generics in Go."},
})

// NewStore creates a Store seeded with the given posts.
func NewStore(seed []Post) *Store {
	s := &Store{posts: make(map[string]Post, len(seed))}
	for _, p := range seed {
		s.posts[p.PostID] = p
	}
	return s
}

// Get returns the post with the given ID.
func (s *Store) Get(postID string) (Post, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.posts[postID]
	return p, ok
}

// Search returns every post whose title or body contains query, case-insensitively.
func (s *Store) Search(query string) []Post {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query = strings.ToLower(query)
	var results []Post
	for _, p := range s.posts {
		if strings.Contains(strings.ToLower(p.Title), query) || strings.Contains(strings.ToLower(p.Body), query) {
			results = append(results, p)
		}
	}
	return results
}

// PostSearchArgs is the input schema for the "post.search" tool.
type PostSearchArgs struct {
	Query string `json:"query"`
}

// PostSearch searches posts by title/body substring match.
func PostSearch(_ context.Context, args json.RawMessage) (any, error) {
	var in PostSearchArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("blog_post.search: invalid arguments: %w", err)
	}

	return defaultStore.Search(in.Query), nil
}

// PostGetArgs is the input schema for the "post.get" tool.
type PostGetArgs struct {
	PostID string `json:"postID"`
}

// PostGet retrieves a single post by ID.
func PostGet(_ context.Context, args json.RawMessage) (any, error) {
	var in PostGetArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("blog_post.get: invalid arguments: %w", err)
	}

	post, ok := defaultStore.Get(in.PostID)
	if !ok {
		return nil, fmt.Errorf("blog_post.get: post %q not found", in.PostID)
	}

	return post, nil
}
