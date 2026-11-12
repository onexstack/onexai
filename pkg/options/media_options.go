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
	"errors"
	"fmt"
	"time"

	"github.com/spf13/pflag"

	"github.com/onexstack/onexai/pkg/media"
)

// MediaOptions configures where image bytes are stored and how a remote image
// is fetched.
//
// It is shared by every service that handles an image, because the store is
// one store: oai-agent-apiserver owns it, and a service that only forwards a
// reference still needs to know where a reference points.
type MediaOptions struct {
	// Driver selects the store: "local" for a directory on this machine, "cos"
	// for a Tencent COS bucket, or "" to disable image storage entirely.
	//
	// Disabled is a supported deployment: a service with no media block still
	// answers every text request, and refuses an image with a message that
	// names the configuration rather than failing somewhere deeper. That is
	// what keeps this from being a required setting.
	//
	// local and cos differ in what they survive. A directory is readable only
	// from the machine that wrote it, so a second replica serves nothing and
	// local is a single-process choice; a bucket is reachable from every
	// replica, which is what a deployment with more than one process needs.
	Driver string `json:"driver" mapstructure:"driver"`
	// BucketURL is the COS bucket endpoint the cos driver writes to, for
	// example "<name>-<appid>.cos.<region>.myqcloud.com". The scheme is
	// optional; https is assumed when it is omitted. Ignored by local.
	BucketURL string `json:"bucket-url" mapstructure:"bucket-url"`
	// Prefix namespaces the object keys the cos driver writes inside the
	// bucket, for example "agent/images". It is a path *within* the bucket, not
	// a domain: the address a client reaches the bucket at is PublicBaseURL.
	//
	// Empty writes at the bucket root. Choosing one is a decision rather than a
	// tuning knob -- the stored references keep the key the bundle built, so
	// changing the prefix later points them at objects that are no longer
	// there, and the bytes have to be moved with it.
	Prefix string `json:"prefix" mapstructure:"prefix"`
	// SecretID and SecretKey are the COS credentials that sign every request.
	// Both are required by the cos driver and ignored by local.
	//
	// SecretKey is the secret: zero it in a config file that is shared, and
	// note that a key name matching "secret" is already redacted from any
	// config this process prints.
	SecretID  string `json:"secret-id" mapstructure:"secret-id"`
	SecretKey string `json:"secret-key" mapstructure:"secret-key"`
	// DataDir is the directory the local driver writes objects to. Empty means
	// the server's home directory (<home>/.onexai/media). Ignored by cos.
	DataDir string `json:"data-dir" mapstructure:"data-dir"`
	// PublicBaseURL is the address a client reaches this service at, used to
	// render absolute object URLs. Empty yields relative "/media/…" URLs, which
	// is what a deployment behind a single origin wants.
	//
	// It must be an address the *client* can reach, not a bind address: an
	// object URL built from 0.0.0.0 is a link that goes nowhere.
	//
	// For cos it overrides the domain object URLs are built from, which is what
	// a CDN or an accelerated domain sets. Note that a bucket which is not
	// publicly readable answers 403 to the URLs this renders; that deployment
	// reads the bytes back through the service's own content route instead.
	PublicBaseURL string `json:"public-base-url" mapstructure:"public-base-url"`
	// SignKey is the secret local object URLs are signed with. Empty leaves
	// them unsigned. Ignored by cos, which refuses a set one rather than
	// leaving a key that looks like it signs URLs it does not.
	//
	// The signature does not expire — see media.LocalStore.signature for why a
	// stored URL cannot carry an expiry — so this is an unguessability measure
	// rather than an access control. Zero it in a config file that is shared.
	SignKey string `json:"sign-key" mapstructure:"sign-key"`
	// MaxFetchBytes bounds a fetched remote image. Zero uses the package
	// default.
	//
	// It is separate from the per-kind storage ceilings because the two bound
	// different things: this bounds what this process will pull into memory
	// from a URL it did not choose, while the kind ceilings bound what it will
	// keep.
	MaxFetchBytes int64 `json:"max-fetch-bytes" mapstructure:"max-fetch-bytes"`
	// Retention is how long a stored image is kept before the collector may
	// reclaim it. Zero — the default — means never, and it is the default
	// because the number is a policy rather than a tuning knob: it decides how
	// long a user's screenshot stays readable in a conversation they can still
	// scroll back to.
	//
	// In a config file it is a Go duration string, so it must parse: "0s" is
	// the way to spell never, and an omitted or empty value is a startup error
	// rather than "unset" — the decode hook calls time.ParseDuration on
	// whatever is written there.
	//
	// It is applied when the object is stored, and written into the row, so
	// changing it later does not retroactively expire anything — an image kept
	// under the old policy keeps the deadline it was given. That is deliberate:
	// a settings change that silently deletes history is worse than one that
	// applies to what comes next.
	Retention time.Duration `json:"retention" mapstructure:"retention"`
	// GCEnabled turns the collector on. Off by default for the same reason the
	// retention is zero: nothing is reclaimed until an operator has decided the
	// policy. With GCEnabled false the sweep is not even started.
	GCEnabled bool `json:"gc-enabled" mapstructure:"gc-enabled"`
	// GCInterval is how often the collector runs. Zero uses the collector's
	// default.
	GCInterval time.Duration `json:"gc-interval" mapstructure:"gc-interval"`
	// GCBatch bounds how many assets one pass reclaims. Zero uses the default.
	GCBatch int `json:"gc-batch" mapstructure:"gc-batch"`
	// AllowPrivateHosts permits fetching an image from a private, loopback, or
	// link-local address.
	//
	// Default false, and the default is the safe one: a URL naming an internal
	// address is a request this process would make from inside the network,
	// which is how an instance's metadata service gets reached. A deployment
	// whose images live on an internal host turns this on deliberately.
	AllowPrivateHosts bool `json:"allow-private-hosts" mapstructure:"allow-private-hosts"`
}

// NewMediaOptions creates MediaOptions with default values.
//
// The defaults are deliberately inert: an empty driver means a service that
// was never configured for images starts and serves text, rather than writing
// to a directory nobody chose.
func NewMediaOptions() *MediaOptions {
	return &MediaOptions{Driver: ""}
}

// Enabled reports whether a store is configured.
func (o *MediaOptions) Enabled() bool { return o != nil && o.Driver != "" }

// StoreConfig renders the options as the store builder's input.
//
// It exists so the driver rules live with the driver: this file states what a
// deployment wrote, and media.NewStore is the one place that knows what a
// driver needs and how it is built. Without it, every rule about a driver would
// have to be written here as well, and the two copies would be free to disagree
// about what a valid cos configuration is.
func (o *MediaOptions) StoreConfig() media.StoreConfig {
	return media.StoreConfig{
		Driver:        o.Driver,
		DataDir:       o.DataDir,
		PublicBaseURL: o.PublicBaseURL,
		SignKey:       []byte(o.SignKey),
		BucketURL:     o.BucketURL,
		SecretID:      o.SecretID,
		SecretKey:     o.SecretKey,
		Prefix:        o.Prefix,
	}
}

// Validate checks the MediaOptions fields.
func (o *MediaOptions) Validate() []error {
	if o == nil || o.Driver == "" {
		return nil
	}
	// The driver's own rules come from the driver, so a fact about cos is
	// stated once. What this layer adds is the policy that is about the
	// options rather than the store — reclamation, bounds — which no driver
	// has an opinion about.
	errs := media.ValidateStoreConfig(o.StoreConfig())

	// Reclamation and retention are one policy told in two fields, so each
	// setting only means something in the presence of the other. The two ways to
	// state half of it are refused here rather than left to run as a no-op: each
	// reads, from the config file, exactly like the arrangement that works.
	if o.GCEnabled && o.Retention <= 0 {
		errs = append(errs, errors.New(
			"media: gc-enabled with no retention reclaims nothing; set media.retention (e.g. \"720h\") or turn it off"))
	}
	if !o.GCEnabled && o.Retention > 0 {
		errs = append(errs, errors.New(
			"media: retention is set but gc-enabled is off, so every stored image is stamped with a deadline nothing will honor; set media.gc-enabled true"))
	}
	// The collector substitutes its defaults for a non-positive value, so a
	// negative one silently becomes the default rather than the intent.
	if o.GCInterval < 0 {
		errs = append(errs, fmt.Errorf("media: gc-interval %s is negative", o.GCInterval))
	}
	if o.GCBatch < 0 {
		errs = append(errs, fmt.Errorf("media: gc-batch %d is negative", o.GCBatch))
	}
	if o.MaxFetchBytes < 0 {
		errs = append(errs, fmt.Errorf("media: max-fetch-bytes %d is negative", o.MaxFetchBytes))
	}
	return errs
}

// AddFlags binds MediaOptions to command-line flags.
func (o *MediaOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.StringVar(&o.Driver, fullPrefix+".driver", o.Driver,
		"Image store driver: local, cos, or empty to disable image storage.")
	fs.StringVar(&o.BucketURL, fullPrefix+".bucket-url", o.BucketURL,
		"COS bucket endpoint for driver=cos, e.g. <name>-<appid>.cos.<region>.myqcloud.com (scheme optional, https by default).")
	fs.StringVar(&o.Prefix, fullPrefix+".prefix", o.Prefix,
		"Key prefix objects are written under inside the bucket for driver=cos. Empty = bucket root.")
	fs.StringVar(&o.SecretID, fullPrefix+".secret-id", o.SecretID,
		"COS SecretId for driver=cos.")
	fs.StringVar(&o.SecretKey, fullPrefix+".secret-key", o.SecretKey,
		"COS SecretKey for driver=cos. Zero it in a config file that is shared.")
	fs.StringVar(&o.DataDir, fullPrefix+".data-dir", o.DataDir,
		"Directory the local image store writes to. Empty = <home>/.onexai/media.")
	fs.StringVar(&o.PublicBaseURL, fullPrefix+".public-base-url", o.PublicBaseURL,
		"Base URL object links are built from. Empty = relative /media/... URLs.")
	fs.StringVar(&o.SignKey, fullPrefix+".sign-key", o.SignKey,
		"Secret object URLs are signed with. Empty = unsigned.")
	fs.Int64Var(&o.MaxFetchBytes, fullPrefix+".max-fetch-bytes", o.MaxFetchBytes,
		"Ceiling for a fetched remote image, in bytes. 0 = package default.")
	fs.DurationVar(&o.Retention, fullPrefix+".retention", o.Retention,
		"How long a stored image is kept. 0 = never (default).")
	fs.BoolVar(&o.GCEnabled, fullPrefix+".gc-enabled", o.GCEnabled,
		"Reclaim expired images. Off by default; nothing is deleted until a retention is set.")
	fs.DurationVar(&o.GCInterval, fullPrefix+".gc-interval", o.GCInterval, "How often the image collector runs.")
	fs.IntVar(&o.GCBatch, fullPrefix+".gc-batch", o.GCBatch, "Images reclaimed per pass.")
	fs.BoolVar(&o.AllowPrivateHosts, fullPrefix+".allow-private-hosts", o.AllowPrivateHosts,
		"Allow fetching images from private/loopback addresses (SSRF risk; off by default).")
}
