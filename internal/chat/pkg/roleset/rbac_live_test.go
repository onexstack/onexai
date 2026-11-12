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

package roleset

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/pkg/declclient"
)

// TestRBACSourceLive checks that the role lists are the bindings: a user named
// in a binding has the role, and removing the binding takes it away.
func TestRBACSourceLive(t *testing.T) {
	kubeconfig := os.Getenv("ONEXAI_CONTROLPLANE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set ONEXAI_CONTROLPLANE_KUBECONFIG to run against a live oai-declserver")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := declclient.New(kubeconfig, "default", 30*time.Second)
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx, 10*time.Second))

	bindings := client.KubeClient.RbacV1().ClusterRoleBindings()
	create := func(name, role, user string) {
		_, err := bindings.Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = bindings.Delete(context.Background(), name, metav1.DeleteOptions{})
		})
	}
	// The subjects are test-only: a deployment's own bindings (its SRE and admin
	// lists) would otherwise decide these assertions, and the revoke case would
	// fail for a reason that has nothing to do with the code.
	create("live-sre-binding", RoleSRE, "live-sre-user")
	create("live-admin-binding", RoleAdmin, "admin2")

	c := NewLive(NewRBACSource("default", client.ClusterRoleBindings(), client.RoleBindings("default")))
	require.NoError(t, c.Start(ctx))

	require.Eventually(t, func() bool { return c.IsSREUser("live-sre-user") },
		15*time.Second, 100*time.Millisecond, "the SRE binding never reached the role list")
	// An SRE is an administrator by the package's own rule, so the admin
	// predicate is true for an SRE; what must not happen is the reverse.
	assert.True(t, c.IsAdminUser("live-sre-user"), "SRE is a subset of admin by design")
	assert.True(t, c.IsAdminUser("admin2"))
	assert.False(t, c.IsSREUser("admin2"), "an admin binding must not grant the SRE role")

	// Revoking the binding takes the role away without a restart.
	require.NoError(t, bindings.Delete(ctx, "live-sre-binding", metav1.DeleteOptions{}))
	require.Eventually(t, func() bool { return !c.IsSREUser("live-sre-user") },
		15*time.Second, 100*time.Millisecond, "a revoked binding kept granting the role")
}
