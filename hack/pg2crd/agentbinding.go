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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	chatmodel "github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/command"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/dbschema"
)

// migrateAgentBindings turns the legacy chat.agent_command rows into
// AgentBinding objects.
//
// # Why this has no XxxToCR to reuse
//
// Every other kind here converts through the function the runtime's write path
// uses, so a migrated object is indistinguishable from one the admin API wrote.
// Command grants have no such function: the table's writer was deleted when
// AgentBinding took over (internal/chat/store/agent_command.go is gone), and the
// binding is now an object the *agent* service owns. So the mapping lives here,
// written against the two shapes it joins.
//
// # What the table carries that the object does not
//
// `command_selector` grants a whole group of catalogue commands by label
// (`agent.onexstack.io/group in (core,duty,…)`). AgentBinding has the same field
// today (`spec.commandSelector`), but this conversion still expands it into
// names: a migrated object should mean exactly what the row meant on the day it
// was migrated, and expansion is the same walk
// internal/chat/pkg/agentcmd/rule.go does at runtime. A grant that should follow
// the catalogue can be rewritten as a selector afterwards; the two are unioned,
// so nothing is lost by leaving the expanded names in place.
//
// `agent_field_selector` has no AgentBinding equivalent at all. It is converted
// when it names `metadata.name`, and refused otherwise: silently dropping one
// dimension of a grant would turn "these agents may run X" into "everyone may"
// or "nobody may" without saying so.
func migrateAgentBindings(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	// The model has no TableName(): it is an in-memory projection now, not a
	// table. The physical name therefore comes from dbschema, which the DSN in
	// the service config has already configured.
	table := dbschema.Table(dbschema.Chat, "agent_command")

	var rows []chatmodel.AgentCommandM
	if err := db.WithContext(ctx).Table(table).Find(&rows).Error; err != nil {
		return fmt.Errorf("read %s: %w", table, err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	for i := range rows {
		// A binding lives in the namespace of the agents it selects: the runtime
		// matches a binding against agents in its own namespace, and a binding
		// it cannot see selects nothing.
		namespace := place.namespaceOf(kindAgentBinding, rows[i].Name)
		obj, err := agentCommandToBinding(&rows[i], namespace)
		if err != nil {
			return err
		}
		out.add("40-agentbindings.yaml", kindAgentBinding, namespace, obj, existing)
	}
	return nil
}

// agentCommandToBinding converts one row. The name is carried over verbatim so
// the migration is traceable: an operator can read `chat.agent_command` and
// `kubectl get agentbinding` side by side.
func agentCommandToBinding(row *chatmodel.AgentCommandM, namespace string) (*agentv1alpha1.AgentBinding, error) {
	enabled := row.Enabled()

	spec := agentv1alpha1.AgentBindingSpec{
		Enabled:     &enabled,
		DisplayName: row.DisplayName,
		Description: row.Description,
		Agents:      row.AgentList(),
	}

	// Label selector: the two shapes are the same, only the representation
	// differs (a stored string vs. matchExpressions).
	if row.AgentSelector != "" {
		selector, err := labelSelectorFromString(row.AgentSelector)
		if err != nil {
			return nil, fmt.Errorf("binding %q: agent selector %q: %w", row.Name, row.AgentSelector, err)
		}
		spec.Selector = selector
	}

	// Field selector: no equivalent, so it is converted to names or refused.
	if row.AgentFieldSelector != "" {
		names, err := agentNamesFromFieldSelector(row.AgentFieldSelector)
		if err != nil {
			return nil, fmt.Errorf("binding %q: agent field selector %q cannot be expressed as an AgentBinding: %w",
				row.Name, row.AgentFieldSelector, err)
		}
		spec.Agents = append(spec.Agents, names...)
	}

	commands, err := grantedCommands(row)
	if err != nil {
		return nil, err
	}
	spec.Commands = commands

	return &agentv1alpha1.AgentBinding{
		TypeMeta:   typeMeta("agent", "AgentBinding"),
		ObjectMeta: objectMeta(row.Name, namespace),
		Spec:       spec,
	}, nil
}

// grantedCommands is the union of the commands named outright and the commands
// the selector grants, expanded against the compiled catalogue.
func grantedCommands(row *chatmodel.AgentCommandM) ([]string, error) {
	set := map[string]struct{}{}

	if row.CommandSelector != "" {
		selector, err := labels.Parse(row.CommandSelector)
		switch {
		case err == nil:
			for name, commandLabels := range command.AllLabels() {
				if selector.Matches(commandLabels) {
					set[name] = struct{}{}
				}
			}
		default:
			// Some rows wrote a JSON list of command names into this column
			// instead of a selector (the pre-AgentBinding admin API did). The
			// meaning is not in doubt, so it is read as what it says rather than
			// refused — refusing would drop a grant the operator made.
			var names []string
			if jsonErr := json.Unmarshal([]byte(row.CommandSelector), &names); jsonErr != nil {
				return nil, fmt.Errorf("binding %q: command selector %q is neither a selector nor a JSON name list: %w",
					row.Name, row.CommandSelector, err)
			}
			for _, name := range names {
				if name != "" {
					set[name] = struct{}{}
				}
			}
		}
	}

	for _, name := range row.CommandList() {
		if name == "" {
			continue
		}
		set[name] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// labelSelectorFromString converts a stored selector string into the object
// form. labels.Parse normalises `==` to `=`, so the requirements it yields are
// already in the shape matchExpressions takes.
func labelSelectorFromString(s string) (*metav1.LabelSelector, error) {
	selector, err := labels.Parse(s)
	if err != nil {
		return nil, err
	}

	out := &metav1.LabelSelector{}
	requirements, _ := selector.Requirements()
	for _, requirement := range requirements {
		var operator metav1.LabelSelectorOperator
		switch requirement.Operator() {
		case selection.In, selection.Equals, selection.DoubleEquals:
			operator = metav1.LabelSelectorOpIn
		case selection.NotIn, selection.NotEquals:
			operator = metav1.LabelSelectorOpNotIn
		case selection.Exists:
			operator = metav1.LabelSelectorOpExists
		case selection.DoesNotExist:
			operator = metav1.LabelSelectorOpDoesNotExist
		default:
			return nil, fmt.Errorf("unsupported operator %q", requirement.Operator())
		}
		values := requirement.Values().UnsortedList()
		sort.Strings(values)
		out.MatchExpressions = append(out.MatchExpressions, metav1.LabelSelectorRequirement{
			Key:      requirement.Key(),
			Operator: operator,
			Values:   values,
		})
	}
	sort.Slice(out.MatchExpressions, func(i, j int) bool {
		return out.MatchExpressions[i].Key < out.MatchExpressions[j].Key
	})
	return out, nil
}

// agentNamesFromFieldSelector extracts the agents a legacy field selector named.
//
// Only `metadata.name` is expressible. Two stored shapes are accepted because
// the column has carried both: a field selector string (`metadata.name in (a,b)`
// or `metadata.name=a`) and a bare JSON list of names, which is what the
// pre-AgentBinding admin API wrote. Anything else is an error rather than a
// silent omission — a dropped dimension silently widens or voids the grant.
func agentNamesFromFieldSelector(raw string) ([]string, error) {
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		sort.Strings(list)
		return list, nil
	}

	names, err := parseNameFieldSelector(raw)
	if err != nil {
		return nil, fmt.Errorf("neither a field selector nor a JSON name list: %w", err)
	}
	sort.Strings(names)
	return names, nil
}

// parseNameFieldSelector understands the two spellings the chat rule compiler
// accepted: `metadata.name=a` and `metadata.name in (a,b)`.
func parseNameFieldSelector(raw string) ([]string, error) {
	const key = "metadata.name"
	s := strings.TrimSpace(raw)

	if rest, ok := strings.CutPrefix(s, key+" in ("); ok {
		rest, ok = strings.CutSuffix(rest, ")")
		if !ok {
			return nil, fmt.Errorf("unterminated `in` list")
		}
		names := make([]string, 0, 4)
		for _, name := range strings.Split(rest, ",") {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
		return names, nil
	}

	for _, prefix := range []string{key + "==", key + "="} {
		if name, ok := strings.CutPrefix(s, prefix); ok {
			if name = strings.TrimSpace(name); name == "" {
				return nil, fmt.Errorf("empty value")
			}
			return []string{name}, nil
		}
	}

	return nil, fmt.Errorf("only %s is expressible", key)
}
