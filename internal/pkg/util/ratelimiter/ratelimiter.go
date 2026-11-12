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

package ratelimiter

import (
	"time"

	"golang.org/x/time/rate"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// DefaultControllerRateLimiter returns the rate limiter shared by all OneX controllers.
func DefaultControllerRateLimiter() workqueue.TypedRateLimiter[reconcile.Request] {
	return workqueue.NewTypedMaxOfRateLimiter(
		// Per-item exponential backoff: retries start at 200ms and grow to at most 1h,
		// so a persistently failing object is retried less and less frequently.
		workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](200*time.Millisecond, 1*time.Hour),
		// Overall token bucket: 5000 qps with a burst of 10000. This is the global,
		// not per-item, cap on retry throughput.
		&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(5000), 10000)},
	)
}
