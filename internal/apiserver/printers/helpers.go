// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package printers

import (
	"strconv"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
)

const (
	// emptyCell stands in for a field the object left unset, the way kubectl
	// prints <none> for an empty column.
	emptyCell = "<none>"
	// unknownAge is what a zero creation timestamp renders as.
	unknownAge = "<unknown>"
	// terminating is the STATUS of an object being deleted.
	terminating = "Terminating"

	// conditionReady is the aggregate condition every resource except
	// AuthorizationRule reports. Only the versioned package exports it, and the
	// convertors deliberately work on the internal types.
	conditionReady = "Ready"

	statusReady    = "Ready"
	statusNotReady = "NotReady"
	statusUnknown  = "Unknown"
)

// humanAge renders a creation timestamp the way kubectl's AGE column does.
func humanAge(created metav1.Time, now time.Time) string {
	if created.IsZero() {
		return unknownAge
	}

	// A creation timestamp in the future means the clock is skewed, not that the
	// object is <invalid>; clamp it rather than print that.
	return duration.HumanDuration(max(now.Sub(created.Time), 0))
}

// conditionStatus maps one condition onto the STATUS vocabulary, so every
// resource reads the same whatever condition type it keys on.
func conditionStatus(conditions []metav1.Condition, conditionType string) string {
	condition := apimeta.FindStatusCondition(conditions, conditionType)
	if condition == nil {
		return statusUnknown
	}

	switch condition.Status {
	case metav1.ConditionTrue:
		return statusReady
	case metav1.ConditionFalse:
		return statusNotReady
	default:
		return statusUnknown
	}
}

// orNone renders an empty string as the placeholder cell.
func orNone(value string) string {
	if value == "" {
		return emptyCell
	}

	return value
}

// selectorString renders a LabelSelector the way labels.Parse would spell it.
// An explicitly empty selector means "everything in the namespace"; printing it
// as an empty cell would hide the most consequential selector a binding can
// have.
func selectorString(selector *metav1.LabelSelector) string {
	if selector == nil {
		return emptyCell
	}
	parsed, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return emptyCell
	}
	if parsed.Empty() {
		return "<all>"
	}

	return parsed.String()
}

// joinNames renders a name list as one cell.
func joinNames(names []string) string {
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if name != "" {
			kept = append(kept, name)
		}
	}

	if len(kept) == 0 {
		return emptyCell
	}

	return strings.Join(kept, ",")
}

// ownerName renders spec.owner.name.
func ownerName(owner *rbacv1.Subject) string {
	if owner == nil {
		return emptyCell
	}

	return orNone(owner.Name)
}

// optInt32 renders an optional integer field.
func optInt32(value *int32) string {
	if value == nil {
		return emptyCell
	}

	return strconv.FormatInt(int64(*value), 10)
}

// optFloat64 renders an optional floating point field without trailing zeros.
func optFloat64(value *float64) string {
	if value == nil {
		return emptyCell
	}

	return strconv.FormatFloat(*value, 'f', -1, 64)
}
