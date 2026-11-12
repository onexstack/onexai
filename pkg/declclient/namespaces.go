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

package declclient

import (
	"context"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Namespaces lists the control plane's namespaces, sorted by name.
//
// It is a live read rather than an informer: namespaces are few and change
// rarely, and the console that asks for them caches the answer for a short
// while. Adding another informer would cost a watch, a cache sync and a
// start-up failure mode for a list this small.
//
// A nil client or a client without a Kubernetes handle answers an empty list
// instead of an error: a caller that only needs selectable options should not
// fail when the control plane is not configured.
func (c *Client) Namespaces(ctx context.Context) ([]string, error) {
	if c == nil || c.KubeClient == nil {
		return []string{}, nil
	}
	list, err := c.KubeClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.Name)
	}
	sort.Strings(names)
	return names, nil
}
