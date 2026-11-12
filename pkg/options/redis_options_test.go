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
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// TestRedisOptionsYAMLDecode is the test the embedded options exist for.
//
// The connection keys live in onexstack's RedisOptions and reach this struct
// through `mapstructure:",squash"`. If that squash does not apply, every one of
// them silently decodes to its zero value — the address included — and the
// symptom is a service that thinks Redis is unconfigured and quietly keeps its
// sessions in process memory. Nothing errors; conversations simply stop being
// shared between pods.
//
// The decode target is a wrapper carrying a "redis" key, mirroring how the
// server holds this struct: decoding straight into a *RedisOptions would look
// for addr/database/... at the config root and find nothing.
func TestRedisOptionsYAMLDecode(t *testing.T) {
	const yamlConfig = `
redis:
  addr: "10.0.0.5:6379"
  username: "other.redis"
  password: "s3cret"
  database: 5
  dial-timeout: 2s
  read-timeout: 1s
  write-timeout: 1s
  max-retries: 1
  key-prefix: "onexai"
  session-ttl: "24h"
`
	var wrapper struct {
		Redis *RedisOptions `mapstructure:"redis"`
	}
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(yamlConfig)); err != nil {
		t.Fatalf("read config: %v", err)
	}
	if err := v.Unmarshal(&wrapper); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wrapper.Redis == nil {
		t.Fatal("the redis block decoded to nil")
	}

	got := wrapper.Redis
	// The connection half, which crosses the squash boundary.
	if got.Addr != "10.0.0.5:6379" {
		t.Errorf("Addr = %q, want the configured address (the embedded block did not decode)", got.Addr)
	}
	if got.Username != "other.redis" {
		t.Errorf("Username = %q, want other.redis — the ACL user is what the shared servers require", got.Username)
	}
	if got.Password != "s3cret" {
		t.Errorf("Password = %q, want the configured one", got.Password)
	}
	if got.Database != 5 {
		t.Errorf("Database = %d, want 5 (note the key is `database`, not `db`)", got.Database)
	}
	if got.DialTimeout != 2*time.Second || got.ReadTimeout != time.Second || got.WriteTimeout != time.Second {
		t.Errorf("timeouts = %v/%v/%v, want 2s/1s/1s", got.DialTimeout, got.ReadTimeout, got.WriteTimeout)
	}
	if got.MaxRetries != 1 {
		t.Errorf("MaxRetries = %d, want 1", got.MaxRetries)
	}
	// This struct's own half.
	if got.KeyPrefix != "onexai" {
		t.Errorf("KeyPrefix = %q, want onexai", got.KeyPrefix)
	}
	if got.SessionTTL != 24*time.Hour {
		t.Errorf("SessionTTL = %v, want 24h", got.SessionTTL)
	}
	if !got.Enabled() {
		t.Error("Enabled() = false for a fully configured block")
	}
}

// TestRedisOptionsDecodesIntoPreparedDefaults is the path the server actually
// takes.
//
// NewServerOptions builds a RedisOptions with defaults before viper reads the
// file, so the decode lands in a struct that is already populated. Two things
// have to hold at once: the keys that ARE in the file must overwrite their
// defaults, and the ones that are NOT must survive — a decode that reset the
// struct would empty key-prefix and session-ttl on every deployment that
// configured only an address, and the symptom would be keys written without
// their prefix rather than an error.
func TestRedisOptionsDecodesIntoPreparedDefaults(t *testing.T) {
	const yamlConfig = `
redis:
  addr: "10.0.0.5:6379"
  username: "other.redis"
  database: 5
`
	wrapper := struct {
		Redis *RedisOptions `mapstructure:"redis"`
	}{Redis: NewRedisOptions()}

	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(yamlConfig)); err != nil {
		t.Fatalf("read config: %v", err)
	}
	if err := v.Unmarshal(&wrapper); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wrapper.Redis
	if got == nil {
		t.Fatal("the prepared options were replaced by nil")
	}
	// What the file set.
	if got.Addr != "10.0.0.5:6379" || got.Username != "other.redis" || got.Database != 5 {
		t.Errorf("the configured keys did not overwrite the defaults: %+v", got)
	}
	// What the file did not set, and which must have survived.
	if got.KeyPrefix != "onexai" {
		t.Errorf("KeyPrefix = %q, want the default onexai to survive a partial config", got.KeyPrefix)
	}
	if got.SessionTTL != 24*time.Hour {
		t.Errorf("SessionTTL = %v, want the default 24h to survive a partial config", got.SessionTTL)
	}
	if got.DialTimeout == 0 || got.PoolSize == 0 {
		t.Errorf("the inherited connection defaults were reset: dial=%v pool=%d", got.DialTimeout, got.PoolSize)
	}
}

// TestRedisOptionsDefaultsPointNowhere pins the one place this service departs
// from the platform's defaults.
//
// The generic default address is 127.0.0.1:6379, which is right for a service
// whose Redis is required. Here an empty address is how "no Redis" is spelled,
// so inheriting that default would make every deployment that never asked for
// Redis depend on one that happens to be listening on localhost — and sessions
// would be shared with whatever else is there.
func TestRedisOptionsDefaultsPointNowhere(t *testing.T) {
	o := NewRedisOptions()
	if o.Addr != "" {
		t.Errorf("default Addr = %q, want empty (empty means no Redis)", o.Addr)
	}
	if o.Enabled() {
		t.Error("the default options report themselves enabled")
	}
	if o.KeyPrefix != "onexai" || o.SessionTTL != 24*time.Hour {
		t.Errorf("defaults = prefix %q ttl %v, want onexai/24h", o.KeyPrefix, o.SessionTTL)
	}
	// The platform's connection defaults are still in place underneath, so a
	// deployment that sets only an address gets sensible timeouts rather than
	// zero ones that mean "wait for ever".
	if o.DialTimeout == 0 || o.PoolSize == 0 {
		t.Errorf("the inherited defaults were lost: dial=%v pool=%d", o.DialTimeout, o.PoolSize)
	}
	if (*RedisOptions)(nil).Enabled() {
		t.Error("a nil RedisOptions reports itself enabled")
	}
}

// TestRedisOptionsValidateDerivesTimeouts pins the derivation the platform
// performs: a deployment that sets only read-timeout must not be left with
// zero write and pool timeouts, which go-redis reads as "no limit".
func TestRedisOptionsValidateDerivesTimeouts(t *testing.T) {
	o := NewRedisOptions()
	o.ReadTimeout = time.Second
	o.WriteTimeout = 0
	o.PoolTimeout = 0

	if errs := o.Validate(); len(errs) != 0 {
		t.Fatalf("Validate() = %v, want nil", errs)
	}
	if o.WriteTimeout != time.Second {
		t.Errorf("WriteTimeout = %v, want the read timeout", o.WriteTimeout)
	}
	if o.PoolTimeout <= time.Second {
		t.Errorf("PoolTimeout = %v, want more than the read timeout", o.PoolTimeout)
	}
}
