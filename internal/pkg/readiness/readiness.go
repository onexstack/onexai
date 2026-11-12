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

// Package readiness answers the question a liveness probe cannot: can this
// instance serve traffic right now?
//
// The two probes mean different things and must not share a code path.
// /healthz says the process is alive; a failing database must not make it fail,
// or Kubernetes restarts a pod that is running fine and would have recovered.
// /readyz says the dependencies this instance needs are reachable; failing it
// only takes the pod out of the load balancer, which is the right response to a
// database it cannot reach.
package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// defaultTimeout bounds one probe. A dependency that cannot answer within this
// window is, for routing purposes, down: the caller would have waited for it.
const defaultTimeout = 2 * time.Second

// Check is one dependency whose reachability determines readiness.
type Check struct {
	// Name identifies the dependency in the probe output, e.g. "database".
	Name string
	// Probe returns nil when the dependency is usable.
	Probe func(context.Context) error
}

// Result is the outcome of one check.
type Result struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
	// Detail classifies a failure without repeating the dependency's own error:
	// /readyz is unauthenticated, and a driver error routinely names the host
	// and port it could not reach. The full error goes to the log instead.
	Detail string `json:"detail,omitempty"`
}

// Registry runs the checks in order and reports the aggregate.
type Registry struct {
	checks  []Check
	timeout time.Duration
}

// New builds a registry. A registry with no checks is always ready, which is
// the correct answer for a deployment that depends on nothing it can probe.
func New(checks ...Check) *Registry {
	return &Registry{checks: checks, timeout: defaultTimeout}
}

// Ready runs every probe and reports whether all of them succeeded.
//
// A nil registry reports ready: it means the deployment registered no
// dependency, and a probe endpoint that panics is worse than one that says
// nothing is wrong.
func (r *Registry) Ready(ctx context.Context) (bool, []Result) {
	if r == nil {
		return true, nil
	}

	results := make([]Result, 0, len(r.checks))
	ready := true

	for _, check := range r.checks {
		res := Result{Name: check.Name, Ready: true} //nolint:exhaustruct_v5 // Detail is set only on failure

		probeCtx, cancel := context.WithTimeout(ctx, r.timeout)
		err := check.Probe(probeCtx)
		cancel()

		if err != nil {
			ready = false
			res.Ready = false
			res.Detail = classify(err)
			// The detail is deliberately coarse upstream; the real error is only
			// safe in the log.
			slog.WarnContext(ctx, "readiness probe failed", "check", check.Name, "error", err)
		}

		results = append(results, res)
	}

	return ready, results
}

// classify reduces a probe error to a short, non-identifying reason.
func classify(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "error"
	}
}

// Handler serves the probe over HTTP: 200 when every check passes, 503 when any
// fails. Both carry the per-check breakdown so an operator can see which
// dependency is down without reading logs.
//
// It is a net/http handler rather than a gin one so the package stays usable
// from either transport; mount it with gin.WrapH.
func (r *Registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		type response struct {
			Ready  bool     `json:"ready"`
			Checks []Result `json:"checks"`
		}

		ready, results := r.Ready(req.Context())

		w.Header().Set("Content-Type", "application/json")

		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}

		if err := json.NewEncoder(w).Encode(response{Ready: ready, Checks: results}); err != nil {
			slog.WarnContext(req.Context(), "failed to write readiness response", "error", err)
		}
	}
}
