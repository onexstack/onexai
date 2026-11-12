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
	"github.com/onexstack/onexai/internal/agent/biz/v1/extensions"
	"github.com/onexstack/onexai/pkg/declclient"
)

// ProvideExtensions exposes the control plane's extension objects — admission
// webhook configurations, CEL admission policies and their bindings, and
// APIService — to the console.
//
// It reads and writes through the same declclient every other control-plane
// consumer uses, so the console never needs a second route to oai-declserver,
// and a deployment without a control plane simply has no extension pages.
func ProvideExtensions(client *declclient.Client) extensions.Biz {
	if client == nil || client.DynamicClient == nil {
		return nil
	}
	return extensions.New(client.DynamicClient)
}
