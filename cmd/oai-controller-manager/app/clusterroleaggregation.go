// Copyright 2022 Lingfei Kong <colin404@foxmail.com>
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
//
// The original repo for this file is https://github.com/onexstack/onex.
//

package app

import (
	"context"

	"k8s.io/kubernetes/pkg/controller/clusterroleaggregation"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/onexstack/onexai/cmd/oai-controller-manager/names"
)

// newClusterRoleAggregationControllerDescriptor returns a ControllerDescriptor for
// the clusterrole-aggregation controller, which merges the rules of the ClusterRoles
// referenced by a ClusterRole's aggregationRule into that ClusterRole.
func newClusterRoleAggregationControllerDescriptor() *ControllerDescriptor {
	return &ControllerDescriptor{
		name:    names.ClusterRoleAggregationController,
		aliases: []string{"clusterroleaggregation"},
		addFunc: addClusterRoleAggregationController,
	}
}

// addClusterRoleAggregationController registers the clusterrole-aggregation
// controller as a leader-only manager Runnable. Starting it from the descriptor
// would run it on every replica, because descriptors are added before the
// manager has acquired the lease.
func addClusterRoleAggregationController(_ context.Context, mgr ctrl.Manager, cctx ControllerContext) (bool, error) {
	return true, mgr.Add(&clusterRoleAggregation{cctx: cctx})
}

// clusterRoleAggregation adapts the k8s controller to the manager.Runnable
// contract: it blocks until the context is cancelled.
type clusterRoleAggregation struct {
	cctx ControllerContext
}

// Start implements manager.Runnable.
func (c *clusterRoleAggregation) Start(ctx context.Context) error {
	clusterRoleInformer := c.cctx.InformerFactory.Rbac().V1().ClusterRoles()
	clusterRolesClient := c.cctx.ClientBuilder.ClientOrDie("clusterrole-aggregator").RbacV1()

	controller := clusterroleaggregation.NewClusterRoleAggregation(ctx, clusterRoleInformer, clusterRolesClient)
	go controller.Run(ctx, 5)

	<-ctx.Done()
	return nil
}

// NeedLeaderElection implements manager.LeaderElectionRunnable: writing
// ClusterRole rules from every replica would race.
func (c *clusterRoleAggregation) NeedLeaderElection() bool { return true }
