/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package names holds the canonical controller names.
//
// The names live here rather than in the command package so that a controller
// can name itself the same way the descriptor registers it. Importing cmd from
// internal would invert the dependency, which is why kube-controller-manager
// keeps its own list in a package the controllers can reach.
//
// Generated controllers keep their ControllerName next to the reconciler they
// name; this package is for the controllers every controller-manager ships.
package names

const (
	GarbageCollectorController           = "garbage-collector-controller"
	NamespacedResourcesDeleterController = "namespaced-resource-deleter-controller"
	ClusterRoleAggregationController     = "clusterrole-aggregation-controller"
	// ResourceMirrorController projects control-plane resources into PostgreSQL
	// and keeps the projection honest. Its cleaner is part of the same
	// controller on purpose: a projection that is written but never verified
	// keeps stale rows forever, so the two are one switch.
	ResourceMirrorController = "resource-mirror-controller"
)
