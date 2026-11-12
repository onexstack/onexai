// Copyright 2022 Lingfei Kong <colin404@foxmail.com>
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
//
// The original repo for this file is https://github.com/onexstack/onex.
//

// Package watcher provides functions used by all watchers.
package watcher

import (
	"github.com/onexstack/onexai/internal/jobserver/pkg/clientset/typed/minio"
	"github.com/onexstack/onexai/internal/jobserver/store"
)

// AggregateConfig aggregates the configurations of all watchers and serves as a configuration aggregator.
type AggregateConfig struct {
	Minio minio.Interface
	Store store.IStore
}
