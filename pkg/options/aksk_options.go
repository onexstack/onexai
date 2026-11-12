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

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/spf13/pflag"
)

var _ genericoptions.IOptions = (*AKSKOptions)(nil)

// AKSKOptions holds one caller's AK/SK pair: the key id it signs with, and the
// secret that signs. Both must match a secret row on the receiving service,
// keyed by APIKey — which is why they are configured together rather than
// derived from each other.
type AKSKOptions struct {
	// APIKey is the key id sent as X-API-KEY.
	APIKey string `json:"api-key" mapstructure:"api-key"`
	// SecretKey is the shared secret the signature is computed with.
	SecretKey string `json:"secret-key" mapstructure:"secret-key"`
}

// NewAKSKOptions creates AKSKOptions with empty values: a caller that has not
// configured a pair sends unsigned requests, and the receiving service refuses
// them, which is the loud failure rather than the silent one.
func NewAKSKOptions() *AKSKOptions {
	return &AKSKOptions{}
}

// Enabled reports whether a usable pair is configured.
func (o *AKSKOptions) Enabled() bool {
	return o != nil && o.APIKey != "" && o.SecretKey != ""
}

// Validate checks the pair is either fully set or fully absent. A half-set pair
// is a configuration mistake that would otherwise surface only as a 401.
func (o *AKSKOptions) Validate() []error {
	if o == nil {
		return nil
	}
	if (o.APIKey == "") != (o.SecretKey == "") {
		return []error{fmt.Errorf("aksk: api-key and secret-key must be set together")}
	}
	return nil
}

// AddFlags adds the AK/SK flags to the specified FlagSet.
func (o *AKSKOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.APIKey, fullPrefix+".api-key", o.APIKey,
		"Key id sent as X-API-KEY on outbound signed requests.")
	fs.StringVar(&o.SecretKey, fullPrefix+".secret-key", o.SecretKey,
		"Shared secret the AK/SK signature is computed with.")
}
