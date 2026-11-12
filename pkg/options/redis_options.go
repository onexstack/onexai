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
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/pflag"
)

// RedisOptions holds the Redis connection configuration used by the AI agent
// flow: conversation sessions, cross-pod stop signaling, and the AK/SK nonce
// store.
//
// # Why it wraps the platform's own type
//
// The connection half is onexstack's genericoptions.RedisOptions rather than a
// second spelling of the same fields. That type is what every service in the
// platform is configured with — including the projects this repository's own
// scaffold generates — so a deployment already has one `redis:` block it knows
// how to fill in, and this service reads the same keys.
//
// The reason it matters is concrete rather than tidy. The servers these
// services share are ACL-protected: connecting needs a `username` as well as a
// password, and a hand-rolled options struct that has no username field cannot
// connect to them at all. It also carries the socket timeouts and the retry
// budget, which are the difference between a Redis outage being a refused
// request and being a request that hangs.
//
// The two fields below the embedded block are this service's own: they say how
// the agent flow uses the connection, not how to make one.
type RedisOptions struct {
	genericoptions.RedisOptions `json:",inline" mapstructure:",squash"`

	// KeyPrefix prefixes every key and channel this service writes, so one
	// Redis database can hold several deployments' sessions without them
	// meeting.
	KeyPrefix string `json:"key-prefix" mapstructure:"key-prefix"`
	// SessionTTL bounds a conversation's history. It is the only thing that
	// reclaims a session nobody comes back to.
	SessionTTL time.Duration `json:"session-ttl" mapstructure:"session-ttl"`
}

// NewRedisOptions creates RedisOptions with default values.
//
// # Why the address is emptied
//
// The generic default points at 127.0.0.1:6379, which is right for a service
// whose Redis is not optional. For this one an empty address means "no Redis" —
// the agent flow then keeps sessions and stops in process memory — so leaving
// the generic default in place would make every deployment that never asked for
// Redis start depending on one that happens to be listening on localhost.
func NewRedisOptions() *RedisOptions {
	o := &RedisOptions{
		KeyPrefix:  "onexai",
		SessionTTL: 24 * time.Hour,
	}
	o.RedisOptions = *genericoptions.NewRedisOptions()
	o.Addr = ""
	return o
}

// Enabled reports whether a Redis backend is configured.
func (o *RedisOptions) Enabled() bool { return o != nil && o.Addr != "" }

// NewClient connects to the configured server.
//
// It is a real connection: the platform's constructor pings before returning,
// so a wrong address, a wrong credential or a wrong ACL user is reported here
// rather than at the first request that needs Redis. See ProvideRedisClient for
// what this service does with that report.
func (o *RedisOptions) NewClient() (*redis.Client, error) {
	return o.RedisOptions.NewClient()
}

// Validate checks the RedisOptions fields.
//
// It runs the embedded validation first, which derives the write timeout and
// the pool timeout from the read timeout when they are unset — the same
// derivation every other service gets, so a deployment that sets only
// read-timeout is not left with two zero timeouts that mean "wait for ever".
func (o *RedisOptions) Validate() []error {
	if o == nil {
		return nil
	}
	return o.RedisOptions.Validate()
}

// AddFlags binds RedisOptions to command-line flags.
func (o *RedisOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	o.RedisOptions.AddFlags(fs, fullPrefix)
	fs.StringVar(&o.KeyPrefix, fullPrefix+".key-prefix", o.KeyPrefix, "Prefix for all Redis keys/channels.")
	fs.DurationVar(&o.SessionTTL, fullPrefix+".session-ttl", o.SessionTTL, "Conversation session TTL.")
}
