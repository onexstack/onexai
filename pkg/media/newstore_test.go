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
	"testing"
)

// TestCanonicalDriver pins that an unknown name is refused and that the known
// ones are recognized however they are spelled, which is what lets a config
// that says "COS" work rather than fail at the first image.
func TestCanonicalDriver(t *testing.T) {
	tests := []struct {
		in    string
		want  string
		known bool
	}{
		{"", "", true},
		{"local", "local", true},
		{"cos", "cos", true},
		{"  COS  ", "cos", true},
		{"Local", "local", true},
		{"s3", "", false},
	}
	for _, tt := range tests {
		got, ok := CanonicalDriver(tt.in)
		if got != tt.want || ok != tt.known {
			t.Errorf("CanonicalDriver(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.known)
		}
	}
}

// TestNewStoreDisabled builds nothing and reports no error. A deployment that
// has not decided where images live must still start, which is what keeps
// storage from being a required setting.
func TestNewStoreDisabled(t *testing.T) {
	store, err := NewStore(StoreConfig{})
	if err != nil {
		t.Fatalf("NewStore(disabled) error = %v", err)
	}
	if store != nil {
		t.Fatalf("NewStore(disabled) returned a %s store, want nil", store.Name())
	}
}

// TestNewStoreLocal pins that the choice is made here: a local configuration
// yields a local store that can name where it writes.
func TestNewStoreLocal(t *testing.T) {
	store, err := NewStore(StoreConfig{Driver: "local", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewStore(local) error = %v", err)
	}
	if store.Name() != "local" {
		t.Fatalf("NewStore(local) = %s, want local", store.Name())
	}
	// The startup log line is built from these, so a driver that names nothing
	// would leave the operator without the one fact the line exists for.
	d, ok := store.(Destination)
	if !ok {
		t.Fatal("the local store does not report its destination, so a startup log cannot say where objects went")
	}
	if len(d.Where()) == 0 {
		t.Error("the local store reported an empty destination")
	}
}

// TestNewStoreRefusesBadConfiguration pins that the driver's own rules are
// enforced where the store is built, not only in the options layer — a caller
// that builds a Config directly reaches this path.
func TestNewStoreRefusesBadConfiguration(t *testing.T) {
	if _, err := NewStore(StoreConfig{Driver: "s3"}); !errors.Is(err, ErrUnsupportedDriver) {
		t.Errorf("NewStore(unknown driver) = %v, want ErrUnsupportedDriver", err)
	}
	// cos names a bucket and credentials; neither is optional.
	if _, err := NewStore(StoreConfig{Driver: "cos"}); err == nil {
		t.Error("NewStore(cos without a bucket or credentials) = nil, want an error")
	}
	// A configured public base URL that is a bind address is refused here too,
	// since the same check guards the options layer.
	if _, err := NewStore(StoreConfig{Driver: "local", DataDir: t.TempDir(), PublicBaseURL: "http://0.0.0.0:8080"}); err == nil {
		t.Error("NewStore(local with a bind address) = nil, want an error")
	}
}
