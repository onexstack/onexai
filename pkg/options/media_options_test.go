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

// TestMediaOptionsYAMLDecode pins the YAML spelling of every field, including
// that an empty driver decodes to "disabled" rather than to a zero-valued store
// nobody asked for.
//
// The decode target is a wrapper carrying a "media" key, mirroring how the
// servers hold this struct. Decoding straight into a *MediaOptions would look
// for driver/data-dir/... at the config root and quietly decode nothing,
// leaving every default in place — silence that looks exactly like a working
// config file.
func TestMediaOptionsYAMLDecode(t *testing.T) {
	const yamlConfig = `
media:
  driver: cos
  bucket-url: bucket-appid.cos.ap-guangzhou.myqcloud.com
  prefix: agent/images
  secret-id: an-id
  secret-key: a-key
  data-dir: /var/lib/onexai/media
  public-base-url: http://localhost:8080
  sign-key: s3cret
  max-fetch-bytes: 1048576
  allow-private-hosts: true
`
	var wrapper struct {
		Media *MediaOptions `mapstructure:"media"`
	}
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(yamlConfig)); err != nil {
		t.Fatalf("read config: %v", err)
	}
	if err := v.Unmarshal(&wrapper); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wrapper.Media == nil {
		t.Fatal("the media block decoded to nil")
	}

	got := wrapper.Media
	want := MediaOptions{
		Driver:            "cos",
		BucketURL:         "bucket-appid.cos.ap-guangzhou.myqcloud.com",
		Prefix:            "agent/images",
		SecretID:          "an-id",
		SecretKey:         "a-key",
		DataDir:           "/var/lib/onexai/media",
		PublicBaseURL:     "http://localhost:8080",
		SignKey:           "s3cret",
		MaxFetchBytes:     1 << 20,
		AllowPrivateHosts: true,
	}
	if *got != want {
		t.Errorf("decoded\n got %+v\nwant %+v", *got, want)
	}
}

func TestMediaOptionsEnabled(t *testing.T) {
	if NewMediaOptions().Enabled() {
		t.Error("the default MediaOptions reports itself enabled")
	}
	if (*MediaOptions)(nil).Enabled() {
		t.Error("a nil MediaOptions reports itself enabled")
	}
	if !(&MediaOptions{Driver: "local"}).Enabled() {
		t.Error("a local-driver MediaOptions reports itself disabled")
	}
}

func TestMediaOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    *MediaOptions
		wantErr bool
	}{
		{name: "nil is fine", opts: nil},
		{name: "disabled is fine", opts: &MediaOptions{}},
		{name: "local is fine", opts: &MediaOptions{Driver: "local", DataDir: "/tmp/x"}},
		{
			name: "cos with a bucket and credentials is fine",
			opts: &MediaOptions{
				Driver:    "cos",
				BucketURL: "bucket-appid.cos.ap-guangzhou.myqcloud.com",
				SecretID:  "an-id",
				SecretKey: "a-key",
			},
		},
		{
			// The prefix is the one cos field with a default (the bucket root),
			// so a config that omits it is complete rather than broken.
			name: "cos with a prefix is fine",
			opts: &MediaOptions{
				Driver:    "cos",
				BucketURL: "bucket-appid.cos.ap-guangzhou.myqcloud.com",
				Prefix:    "agent/images",
				SecretID:  "an-id",
				SecretKey: "a-key",
			},
		},
		{
			name:    "cos without a bucket is refused",
			opts:    &MediaOptions{Driver: "cos", SecretID: "an-id", SecretKey: "a-key"},
			wantErr: true,
		},
		{
			name:    "cos without credentials is refused",
			opts:    &MediaOptions{Driver: "cos", BucketURL: "bucket-appid.cos.ap-guangzhou.myqcloud.com"},
			wantErr: true,
		},
		{
			// A sign-key that is set and unused reads as if this process were
			// protecting the objects, when the bucket decides that.
			name: "cos with a sign-key is refused",
			opts: &MediaOptions{
				Driver:    "cos",
				BucketURL: "bucket-appid.cos.ap-guangzhou.myqcloud.com",
				SecretID:  "an-id",
				SecretKey: "a-key",
				SignKey:   "s3cret",
			},
			wantErr: true,
		},
		{name: "an unknown driver is refused", opts: &MediaOptions{Driver: "s3"}, wantErr: true},
		{
			// A bind address is not a destination: an object URL built from it
			// is a link that goes nowhere, and the failure would surface as a
			// connection error rather than as a config mistake.
			name:    "a bind address is refused",
			opts:    &MediaOptions{Driver: "local", PublicBaseURL: "http://0.0.0.0:8080"},
			wantErr: true,
		},
		{
			name: "a bind address is refused for cos too",
			opts: &MediaOptions{
				Driver:        "cos",
				BucketURL:     "bucket-appid.cos.ap-guangzhou.myqcloud.com",
				SecretID:      "an-id",
				SecretKey:     "a-key",
				PublicBaseURL: "http://0.0.0.0:8080",
			},
			wantErr: true,
		},
		{
			// Reclamation and retention are one policy told in two fields. Each
			// half on its own reads, from the config file, exactly like the
			// arrangement that works — so each is refused rather than run as a
			// no-op that looks like a policy.
			name:    "a collector with no retention is refused",
			opts:    &MediaOptions{Driver: "local", GCEnabled: true},
			wantErr: true,
		},
		{
			name:    "a retention with no collector is refused",
			opts:    &MediaOptions{Driver: "local", Retention: 720 * time.Hour},
			wantErr: true,
		},
		{
			name: "a collector with a retention is fine",
			opts: &MediaOptions{Driver: "local", GCEnabled: true, Retention: 720 * time.Hour},
		},
		{
			name:    "a negative gc-interval is refused",
			opts:    &MediaOptions{Driver: "local", GCInterval: -time.Second},
			wantErr: true,
		},
		{
			name:    "a negative gc-batch is refused",
			opts:    &MediaOptions{Driver: "local", GCBatch: -1},
			wantErr: true,
		},
		{
			name:    "a negative max-fetch-bytes is refused",
			opts:    &MediaOptions{Driver: "local", MaxFetchBytes: -1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.opts.Validate()
			if tt.wantErr && len(errs) == 0 {
				t.Fatalf("Validate() = nil, want an error")
			}
			if !tt.wantErr && len(errs) != 0 {
				t.Fatalf("Validate() = %v, want nil", errs)
			}
		})
	}
}
