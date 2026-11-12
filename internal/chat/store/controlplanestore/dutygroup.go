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

package controlplanestore

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/chat/model"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// This file now holds only the conversion between the retired declarative
// DutyGroup and the SQL row. The runtime no longer reads or writes the control
// plane for duty groups; the one-shot hack/crd2pg migration and the historical
// hack/pg2crd tool are the remaining callers, and they need this conversion so
// a migrated row and an old write agree.

// Annotation keys carrying the fields the object has no spec slot for.
const (
	annotationShortDescription = "onexai.io/short-description"
	annotationSource           = "onexai.io/source"
)

// DutyGroupFromCR exposes the CR-to-row conversion to the one-shot migration
// tool.
func DutyGroupFromCR(cr *chatv1alpha1.DutyGroup) model.OncallGroupM {
	return dutyGroupFromCR(cr)
}

func dutyGroupFromCR(cr *chatv1alpha1.DutyGroup) model.OncallGroupM {
	row := model.OncallGroupM{
		Name:               cr.Name,
		DisplayName:        cr.Spec.DisplayName,
		Status:             statusOf(cr.Spec.Enabled),
		LongDesc:           cr.Spec.Description,
		ShortDesc:          annotation(cr.Annotations, annotationShortDescription),
		Agents:             jsonList(cr.Spec.Agents),
		ScheduleID:         cr.Spec.ScheduleID,
		AllocationStrategy: cr.Spec.AllocationStrategy,
		Owner:              subjectName(cr.Spec.Owner),
		IsSplitByBizLine:   boolText(cr.Spec.SplitByBizLine),
		GenerateWeeklyRpt:  boolText(cr.Spec.WeeklyReport),
		Source:             annotation(cr.Annotations, annotationSource),
		WelcomeMessage:     cr.Spec.WelcomeMessage,
		Namespace:          cr.Namespace,
		Order:              int(cr.Spec.Order),
		CreatedAt:          unixMilli(cr.CreationTimestamp),
		UpdatedAt:          unixMilli(cr.CreationTimestamp),
	}
	// A schedule named rather than identified resolves by name in the legacy
	// path, so the row carries that name as its schedule id.
	if row.ScheduleID == "" && cr.Spec.ScheduleRef != nil {
		row.ScheduleID = cr.Spec.ScheduleRef.Name
	}
	return row
}

// DutyGroupToCR is the reverse conversion, kept for hack/pg2crd and for
// migration tests.
func DutyGroupToCR(row *model.OncallGroupM, cr *chatv1alpha1.DutyGroup) {
	cr.Spec.Enabled = enabledOf(row.Status)
	cr.Spec.DisplayName = row.DisplayName
	cr.Spec.Description = row.LongDesc
	cr.Spec.Agents = decodeList(row.Agents)
	cr.Spec.ScheduleID = row.ScheduleID
	cr.Spec.ScheduleRef = nil
	cr.Spec.AllocationStrategy = row.AllocationStrategy
	cr.Spec.WelcomeMessage = row.WelcomeMessage
	cr.Spec.Order = int32(row.Order)
	cr.Spec.SplitByBizLine = row.IsSplitByBizLine == "true"
	cr.Spec.WeeklyReport = row.GenerateWeeklyRpt == "true"
	if row.Owner != "" {
		cr.Spec.Owner = &rbacv1.Subject{Kind: rbacv1.UserKind, Name: row.Owner}
	}
	setAnnotation(cr, annotationShortDescription, row.ShortDesc)
	setAnnotation(cr, annotationSource, row.Source)
}

func annotation(annotations map[string]string, key string) string {
	if annotations == nil {
		return ""
	}
	return annotations[key]
}

func setAnnotation(obj metav1.Object, key, value string) {
	if value == "" {
		delete(obj.GetAnnotations(), key)
		return
	}
	if obj.GetAnnotations() == nil {
		obj.SetAnnotations(map[string]string{})
	}
	obj.GetAnnotations()[key] = value
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return ""
}

func subjectName(subject *rbacv1.Subject) string {
	if subject == nil {
		return ""
	}
	return subject.Name
}
