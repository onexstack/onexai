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

package options

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/pflag"
)

// UsageReportOptions names the ledger this deployment reports finished runs to.
//
// # Which runs this is for
//
// A run that arrived through OneX's own agent service is accounted for there
// already: that service consumes the run's events and writes the rows itself.
// The runs that need this are the ones OneX never sees — the Lark conversations,
// which reach this service directly — because otherwise their consumption exists
// only in this process's metrics and disappears with the pod.
//
// Empty endpoint disables reporting, which is the correct state for a deployment
// with no Lark surface: it has nothing to report that OneX is not already
// counting.
type UsageReportOptions struct {
	// Endpoint is OneX's agent service base URL, e.g. "http://127.0.0.1:8188".
	// The "/internal/v1/agent-usage" path is appended.
	Endpoint string `json:"endpoint" mapstructure:"endpoint"`
	// Token is the shared secret OneX's control API requires, byte for byte the
	// value of its `internal-api.token`. Reporting without it is refused by every
	// request, so it is required wherever Endpoint is set — see Validate.
	Token string `json:"token" mapstructure:"token"`
	// Timeout bounds one report. <=0 means 5s.
	//
	// It is short on purpose: the report is made after the answer has already
	// been delivered, and a bookkeeping hop that is slow must not hold anything
	// up. The number only has to be long enough for a request on a LAN.
	Timeout time.Duration `json:"timeout" mapstructure:"timeout"`
}

// NewUsageReportOptions creates UsageReportOptions with default values.
func NewUsageReportOptions() *UsageReportOptions {
	return &UsageReportOptions{Timeout: 5 * time.Second}
}

// Enabled reports whether a ledger is configured to report to.
func (o *UsageReportOptions) Enabled() bool {
	return o != nil && o.Endpoint != ""
}

// Validate refuses a half-configured reporter.
//
// OneX's route is guarded by a shared token, so an endpoint without one fails
// every run — and it fails in the one place nobody watches, since the report is
// best-effort by design and only logs. Refusing at startup is what turns that
// into a deployment error rather than a ledger that is quietly short.
//
// A token with no endpoint is refused for the same reason in the other
// direction: it says a deployment meant to report and would otherwise not.
func (o *UsageReportOptions) Validate() []error {
	if o == nil {
		return nil
	}
	var errs []error
	switch {
	case o.Endpoint != "" && o.Token == "":
		errs = append(errs, fmt.Errorf("usage-report.token is required when usage-report.endpoint is set"))
	case o.Endpoint == "" && o.Token != "":
		errs = append(errs, fmt.Errorf("usage-report.endpoint is required when usage-report.token is set"))
	}
	if o.Endpoint != "" && !strings.HasPrefix(o.Endpoint, "http://") && !strings.HasPrefix(o.Endpoint, "https://") {
		// Go's url.Parse reads "host:port" as a scheme, and the transport then
		// refuses the request before a packet is sent — the same silent failure
		// the RAGFlow base URL has, and just as invisible from the outside.
		errs = append(errs, fmt.Errorf("usage-report.endpoint %q must include a scheme (http:// or https://)", o.Endpoint))
	}
	return errs
}

// AddFlags adds the usage-report flags to the given FlagSet.
func (o *UsageReportOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.Endpoint, fullPrefix+".endpoint", o.Endpoint,
		"OneX agent service base URL to report finished runs' usage to; empty disables reporting.")
	fs.StringVar(&o.Token, fullPrefix+".token", o.Token,
		"Shared secret for OneX's control API (its internal-api.token).")
	fs.DurationVar(&o.Timeout, fullPrefix+".timeout", o.Timeout,
		"Timeout for one usage report (default 5s).")
}
