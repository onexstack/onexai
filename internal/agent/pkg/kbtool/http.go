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

package kbtool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	kbclient "github.com/onexstack/onexai/internal/agent/pkg/clientset/typed/kb"
)

// postLabels names one provider call in error messages. Prefix is the package
// and provider ("kbtool/dify"), Call is the operation shown when the transport
// fails, and StatusLabel is the name shown on a non-200 response. StatusLabel
// defaults to Call when empty.
type postLabels struct {
	Prefix      string
	Call        string
	StatusLabel string
}

// postJSON is the POST-and-decode scaffold every knowledge-base provider uses.
// The transport lives in the typed kb client; the labels keep each provider's
// existing error wording, so a log line still says which upstream failed.
func postJSON(
	ctx context.Context, client *kbclient.Client, url string, headers map[string]string,
	body, out any, labels postLabels,
) error {
	status, data, err := client.Post(ctx, url, headers, body)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", labels.Prefix, labels.Call, err)
	}
	if status != http.StatusOK {
		label := labels.StatusLabel
		if label == "" {
			label = labels.Call
		}
		return fmt.Errorf("%s: %s returned HTTP %d: %s",
			labels.Prefix, label, status, summarise(data))
	}

	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: decode response: %w", labels.Prefix, err)
	}
	return nil
}
