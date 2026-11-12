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

package media

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StoreConfig is everything a store is built from, rendered from a service's
// options.
//
// The fields are the union of every driver's needs rather than a per-driver
// struct, because a config file states them together: a deployment that once
// ran cos and now runs local keeps the bucket fields in its file, and having
// NewStore ignore them is friendlier than refusing a file it can act on. It is
// also what makes MediaOptions able to live outside this package without a
// switch over the driver.
type StoreConfig struct {
	// Driver selects the store: "local", "cos", or "" to disable storage.
	Driver string
	// DataDir is the local driver's directory. Empty means <home>/.onexai/media.
	DataDir string
	// PublicBaseURL is the address a client reaches the service at, used to
	// render absolute object URLs. Empty yields relative URLs.
	PublicBaseURL string
	// SignKey signs local object URLs. Empty leaves them unsigned.
	SignKey []byte
	// BucketURL, SecretID, SecretKey and Prefix are the cos driver's settings.
	BucketURL string
	SecretID  string
	SecretKey string
	Prefix    string
}

// Destination is implemented by a store that can name where it puts objects.
//
// It is separate from Store, and optional, so a driver with nothing to add logs
// its name and stops: forcing every driver to invent a destination key would be
// a field that exists to satisfy an interface.
type Destination interface {
	// Where returns slog key/value pairs naming the destination.
	Where() []any
}

// driverSpec is one driver: how to check its configuration and how to build it.
type driverSpec struct {
	name     string
	validate func(StoreConfig) []error
	build    func(StoreConfig) (Store, error)
}

// driverSpecs is the closed set of drivers this build knows.
//
// It is a table rather than a registry on purpose. Registering a driver at
// runtime would turn "the config names a driver this build does not have" from
// a startup error into a lookup that fails at the first image, and the set here
// is two entries that change only with a new file, a new constructor and its
// tests — so the indirection a registry buys has nothing to pay for it.
var driverSpecs = []driverSpec{
	{
		name:     "local",
		validate: validateLocal,
		build:    buildLocal,
	},
	{
		name:     "cos",
		validate: validateCOS,
		build:    buildCOS,
	},
}

// CanonicalDriver normalizes a driver name. An empty string is a valid answer
// meaning "disabled"; the bool reports whether the name is one this build knows.
func CanonicalDriver(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", true
	case "local":
		return "local", true
	case "cos":
		return "cos", true
	default:
		return "", false
	}
}

// ValidateStoreConfig checks a store configuration and returns every problem
// with it, so a start-up failure reports them all at once.
//
// A disabled driver is valid and checks nothing: the fields a disabled config
// still carries are not mistakes, since the same file may be shared with a
// deployment that does store images.
func ValidateStoreConfig(cfg StoreConfig) []error {
	name, ok := CanonicalDriver(cfg.Driver)
	if !ok {
		return []error{fmt.Errorf(
			"%w: %q (want local, cos, or empty to disable)", ErrUnsupportedDriver, cfg.Driver)}
	}
	if name == "" {
		return nil
	}

	var errs []error
	// A bind address is not a destination. The client would be handed a URL to
	// 0.0.0.0 and fail with a connection error that says nothing about the
	// cause, so it is refused where the cause is still visible.
	if strings.Contains(cfg.PublicBaseURL, "0.0.0.0") || strings.Contains(cfg.PublicBaseURL, "::") {
		errs = append(errs, fmt.Errorf(
			"media: public-base-url %q is a bind address; use an address the client can reach", cfg.PublicBaseURL))
	}
	return append(errs, specFor(name).validate(cfg)...)
}

// NewStore builds the store a configuration names.
//
// It is where the driver choice is made, and the only place in this package
// that is: nothing above it knows which driver is configured, and a new driver
// is a new entry in driverSpecs rather than a new branch at every caller.
//
// A disabled driver returns (nil, nil) rather than an error. A deployment that
// has not decided where its images live still answers every text request, which
// is what keeps storage from being a required setting; the callers that need a
// store check for this nil, which is what an unconfigured Bundle reports.
func NewStore(cfg StoreConfig) (Store, error) {
	name, ok := CanonicalDriver(cfg.Driver)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedDriver, cfg.Driver)
	}
	if name == "" {
		return nil, nil
	}
	// Validated here as well as at start-up: NewStore is reachable without the
	// options layer (a test builds a Config directly), and building a store from
	// a configuration its own driver would reject is not a state worth reaching.
	if errs := ValidateStoreConfig(cfg); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return specFor(name).build(cfg)
}

// specFor returns the spec for a canonical driver name. The caller has already
// canonicalized, so an unknown name here is a programming error rather than a
// configuration one.
func specFor(name string) driverSpec {
	for _, s := range driverSpecs {
		if s.name == name {
			return s
		}
	}
	panic("media: no driver spec for canonical name " + name)
}

// validateLocal checks the local driver's configuration.
//
// Nothing is required: an empty data-dir resolves to a directory under the
// server's home, and a sign-key is optional by design.
func validateLocal(StoreConfig) []error { return nil }

// validateCOS checks the cos driver's configuration.
func validateCOS(cfg StoreConfig) []error {
	var errs []error
	if strings.TrimSpace(cfg.BucketURL) == "" {
		errs = append(errs, errors.New(
			"media: cos driver needs bucket-url (<name>-<appid>.cos.<region>.myqcloud.com)"))
	}
	if strings.TrimSpace(cfg.SecretID) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		errs = append(errs, errors.New("media: cos driver needs secret-id and secret-key"))
	}
	// A sign-key that is set and unused is worse than one that is missing: it
	// says the object URLs are signed when the bucket, not this process, decides
	// who may read them. Refusing it where the mistake is visible beats a config
	// that reads as if it were protecting something.
	if len(cfg.SignKey) > 0 {
		errs = append(errs, errors.New(
			"media: sign-key signs local URLs; the cos driver cannot honor it, remove it"))
	}
	return errs
}

// buildLocal builds the directory store.
func buildLocal(cfg StoreConfig) (Store, error) {
	dir := cfg.DataDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("media: resolve home for the default data directory: %w", err)
		}
		dir = filepath.Join(home, ".onexai", "media")
	}
	return NewLocalStore(dir, cfg.PublicBaseURL, cfg.SignKey)
}

// buildCOS builds the bucket store.
func buildCOS(cfg StoreConfig) (Store, error) {
	return NewCOSStore(cfg.BucketURL, cfg.SecretID, cfg.SecretKey, cfg.PublicBaseURL, cfg.Prefix)
}
