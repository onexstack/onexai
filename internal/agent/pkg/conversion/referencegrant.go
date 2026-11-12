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

package conversion

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// ReferenceGrantV1ToCR converts the wire shape to the declarative object.
func ReferenceGrantV1ToCR(p *v1.ReferenceGrant) *agentv1alpha1.ReferenceGrant {
	if p == nil {
		return &agentv1alpha1.ReferenceGrant{}
	}
	cr := &agentv1alpha1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.GetName(),
			Namespace: p.GetNamespace(),
			Labels:    copyLabels(p.GetLabels()),
		},
		Spec: agentv1alpha1.ReferenceGrantSpec{
			From: make([]agentv1alpha1.ReferenceGrantFrom, 0, len(p.GetFrom())),
			To:   make([]agentv1alpha1.ReferenceGrantTo, 0, len(p.GetTo())),
		},
	}
	for _, from := range p.GetFrom() {
		cr.Spec.From = append(cr.Spec.From, agentv1alpha1.ReferenceGrantFrom{
			Group:     from.GetGroup(),
			Kind:      from.GetKind(),
			Namespace: from.GetNamespace(),
		})
	}
	for _, to := range p.GetTo() {
		cr.Spec.To = append(cr.Spec.To, agentv1alpha1.ReferenceGrantTo{
			Group: to.GetGroup(),
			Kind:  to.GetKind(),
			Name:  to.GetName(),
		})
	}
	return cr
}

// ReferenceGrantCRToV1 converts the declarative object to the wire shape.
func ReferenceGrantCRToV1(cr *agentv1alpha1.ReferenceGrant) *v1.ReferenceGrant {
	if cr == nil {
		return nil
	}
	// Both timestamps read the creation time: the object carries no separate
	// update timestamp, matching the other CRD-native resources here.
	createdAt := unixMilli(cr.CreationTimestamp)
	out := &v1.ReferenceGrant{
		Name:      cr.Name,
		Namespace: cr.Namespace,
		From:      make([]*v1.ReferenceGrantFrom, 0, len(cr.Spec.From)),
		To:        make([]*v1.ReferenceGrantTo, 0, len(cr.Spec.To)),
		Labels:    copyLabels(cr.Labels),
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
	for _, from := range cr.Spec.From {
		out.From = append(out.From, &v1.ReferenceGrantFrom{
			Group:     from.Group,
			Kind:      from.Kind,
			Namespace: from.Namespace,
		})
	}
	for _, to := range cr.Spec.To {
		out.To = append(out.To, &v1.ReferenceGrantTo{
			Group: to.Group,
			Kind:  to.Kind,
			Name:  to.Name,
		})
	}
	return out
}
