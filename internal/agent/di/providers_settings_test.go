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

package di

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	customoptions "github.com/onexstack/onexai/pkg/options"
)

// TestSettingsNamespace pins where the settings ConfigMap is looked up: the
// configured namespace, or default when the control plane watches every
// namespace. Getting this wrong makes runtime settings silently unreadable.
func TestSettingsNamespace(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		want      string
	}{
		{name: "a configured namespace is used as is", namespace: "onexai-system", want: "onexai-system"},
		{name: "an unset namespace falls back to default", namespace: "", want: metav1.NamespaceDefault},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := settingsNamespace(&customoptions.ControlPlaneOptions{Namespace: tc.namespace})
			if got != tc.want {
				t.Errorf("settingsNamespace(%q) = %q, want %q", tc.namespace, got, tc.want)
			}
		})
	}
}
