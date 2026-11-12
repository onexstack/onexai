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

package chat

const (
	// Production is the online production environment.
	ProductionEnvironment = "prod"

	// Development is the test environment.
	TestEnvironment = "test"
)

const (
	// CMSNotifyChatIDProd is the Lark chat ID for CMS notify in production.
	CMSNotifyChatIDProd = "oc_b36ad9459a316b59f9a52938d2aa0c72"
	// CMSNotifyChatIDTest is the Lark chat ID for CMS notify in test.
	CMSNotifyChatIDTest = "oc_edb08ecfe368a4bb4f7550e52aaeddf8"
)

// CMSNotifyChatID returns the appropriate Lark chat ID for the given environment.
func CMSNotifyChatID(env string) string {
	if env == ProductionEnvironment {
		return CMSNotifyChatIDProd
	}
	return CMSNotifyChatIDTest
}
