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
	"encoding/json"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	rbacinformers "k8s.io/client-go/listers/rbac/v1"
)

// Role names. A binding whose roleRef names one of these grants the role: the
// name is the contract between whoever writes the binding and this package.
const (
	// RoleSRE is the on-call role: /close, /hand, /assign, /pipeline.
	RoleSRE = "onexai-sre"
	// RoleAdmin is the administrator role: /new, /switch, /sessions, /doctor.
	RoleAdmin = "onexai-admin"
)

// NewRBACSource reads the two role lists from role bindings served by the
// control plane.
//
// ClusterRoleBindings are read cluster-wide; RoleBindings are read in namespace
// only, because a namespaced binding is how a deployment scopes a role to one
// namespace. Only User subjects are considered: the lists name people, and a
// group binding would be an indirection this service has no way to resolve
// (it never sees a caller's groups).
func NewRBACSource(
	namespace string,
	clusterRoleBindings rbacinformers.ClusterRoleBindingLister,
	roleBindings rbacinformers.RoleBindingNamespaceLister,
) Source {
	return &rbacSource{namespace: namespace, clusterRoleBindings: clusterRoleBindings, roleBindings: roleBindings}
}

type rbacSource struct {
	namespace           string
	clusterRoleBindings rbacinformers.ClusterRoleBindingLister
	roleBindings        rbacinformers.RoleBindingNamespaceLister
}

// ListAll returns the two settings rows the parser expects, synthesised from the
// bindings. Keeping the row shape means the parsing, the union and the
// fail-closed defaults stay exactly where they were.
func (s *rbacSource) ListAll(context.Context) ([]*Row, error) {
	sre := map[string]struct{}{}
	admin := map[string]struct{}{}

	clusterBindings, err := s.clusterRoleBindings.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, binding := range clusterBindings {
		collect(binding.RoleRef.Name, binding.Subjects, sre, admin)
	}

	namespaceBindings, err := s.roleBindings.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, binding := range namespaceBindings {
		collect(binding.RoleRef.Name, binding.Subjects, sre, admin)
	}

	return []*Row{
		{Key: KeySREUserIDs, Value: namesJSON(sre)},
		{Key: KeyAdminUserIDs, Value: namesJSON(admin)},
	}, nil
}

func collect(role string, subjects []rbacv1.Subject, sre, admin map[string]struct{}) {
	var target map[string]struct{}
	switch role {
	case RoleSRE:
		target = sre
	case RoleAdmin:
		target = admin
	default:
		return
	}
	for _, subject := range subjects {
		if subject.Kind != rbacv1.UserKind || subject.Name == "" {
			continue
		}
		target[subject.Name] = struct{}{}
	}
}

func namesJSON(set map[string]struct{}) string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return ""
	}
	return string(encoded)
}
