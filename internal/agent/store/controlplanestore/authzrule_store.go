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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
	"github.com/onexstack/onexai/pkg/declclient/declstore"
	agentclient "github.com/onexstack/onexai/pkg/generated/clientset/versioned/typed/agent/v1alpha1"
	agentlisters "github.com/onexstack/onexai/pkg/generated/listers/agent/v1alpha1"
)

// ruleKeyName is the row column a Get/Delete names a rule by.
const ruleKeyName = "name"

// anySubject is the row vocabulary's "any subject" wildcard. On the object it
// is a single subject whose name is "*" — see rowToSubjects.
const anySubject = "*"

// observeTimeout bounds the wait for an informer to observe an object this
// process just wrote. It is the gap between "the API server accepted it" and
// "the OPA rule store can evaluate it": a caller that writes a grant and then
// immediately runs an agent would otherwise be refused by the stale snapshot.
//
// Two seconds is generous for a watch event on an in-cluster API server, and
// the wait is only ever paid on a write — provisioning for a converged account
// is reads only.
const observeTimeout = 2 * time.Second

// observeInterval is how often the wait above re-checks the lister.
const observeInterval = 20 * time.Millisecond

// AuthzRuleStore serves the authorization-rule table over the declarative
// control plane, so the rows the admin API writes are the objects the OPA rule
// store reads.
//
// # Why the rules had to move
//
// The rule source (internal/agent/controlplane.Source) reads
// AuthorizationRule objects, and the write API used to write SQL rows: in a
// control-plane deployment that meant writes went nowhere and reads never saw
// them, which is why the surface was disabled outright. Disabling it, however,
// broke the platform's runtime provisioning: OneX creates a namespace grant and
// a per-user grant through this API before every agent run, and an account that
// cannot be provisioned is refused with 503 AgentNotReady. So the fix is to
// serve the same rows over the objects, one source for both directions.
//
// # Reads are cluster-wide, writes are not
//
// The OPA source lists rules across every namespace (they are a container, not
// part of a rule's meaning), so this store reads the same way. A new object
// lands in namespace (the configured control-plane namespace, else the default
// namespace, which is where the migration placed every existing rule); an
// update or delete first locates the object it already lives in, so a rule
// written by an operator elsewhere is not silently duplicated.
type AuthzRuleStore struct {
	// clientset is the typed client factory; every call narrows to the
	// namespace the object lives in. The API server requires a namespace on
	// Get/Update/Delete — a namespaced resource has no cluster-wide object URL.
	clientset agentclient.AgentV1alpha1Interface
	// lister is the cluster-wide read cache, the same view the OPA source has.
	lister agentlisters.AuthorizationRuleLister
	// namespace is where a new object is created.
	namespace string
}

// Ensure AuthzRuleStore implements the AuthzRuleStore interface.
var _ agentstore.AuthzRuleStore = (*AuthzRuleStore)(nil)

// NewAuthzRuleStore builds the rules accessor. namespace is where new objects
// are written; empty means the default namespace, matching the placement of
// every rule the SQL->CRD migration produced.
func NewAuthzRuleStore(client *declclient.Client, namespace string) *AuthzRuleStore {
	if namespace == "" {
		namespace = model.DefaultNamespace
	}
	return newAuthzRuleStore(client.Clientset.AgentV1alpha1(), client.AuthorizationRules(), namespace)
}

// newAuthzRuleStore is the seam the unit tests use: it takes the typed client
// and the lister directly instead of a declclient, which cannot be built
// against a fake API server.
func newAuthzRuleStore(
	clientset agentclient.AgentV1alpha1Interface,
	lister agentlisters.AuthorizationRuleLister,
	namespace string,
) *AuthzRuleStore {
	return &AuthzRuleStore{clientset: clientset, lister: lister, namespace: namespace}
}

// Create writes a new rule object. A rule whose name already exists is a
// failure (the API server answers AlreadyExists), exactly as the SQL insert
// did: authzrule.Service checks first and reports a duplicate as a validation
// error.
func (s *AuthzRuleStore) Create(ctx context.Context, row *model.AuthzRuleM) error {
	cr := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{Name: row.Name, Namespace: s.namespace},
	}
	if err := applyAuthzRule(row, cr); err != nil {
		return err
	}
	if _, err := s.clientset.AuthorizationRules(s.namespace).Create(ctx, cr, metav1.CreateOptions{}); err != nil {
		return err
	}
	s.waitObserved(ctx, s.namespace, row.Name)
	return nil
}

// Update rewrites the rule as the row describes, keeping the object where it
// already lives. A missing rule is agentstore.ErrNotFound, which is what
// authzrule.Service maps to its own ErrNotFound.
func (s *AuthzRuleStore) Update(ctx context.Context, row *model.AuthzRuleM) error {
	namespace, found, err := s.namespaceOf(ctx, row.Name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: %s", agentstore.ErrNotFound, row.Name)
	}

	api := s.clientset.AuthorizationRules(namespace)
	// Re-read inside the retry: the write is a full-spec overwrite, so it has to
	// carry the resourceVersion the server currently holds, not the one the
	// informer saw a moment ago.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// The write carries the resourceVersion of what the server holds right
		// now, so this read must not be served from a stale cache.
		current, err := api.Get(ctx, row.Name, metav1.GetOptions{}) //nolint:kubelistcheck // must not read a stale cache
		if err != nil {
			return err
		}
		updated := current.DeepCopy()
		if err := applyAuthzRule(row, updated); err != nil {
			return err
		}
		_, err = api.Update(ctx, updated, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return err
	}
	s.waitObserved(ctx, namespace, row.Name)
	return nil
}

// Delete removes the named rule object. A rule that is already gone is
// ErrNotFound, mirroring the SQL store's "no such row" (authzrule.Service
// checks first, so this is the race guard).
func (s *AuthzRuleStore) Delete(ctx context.Context, opts *where.Options) error {
	name, ok := declstore.SingleKey(opts, ruleKeyName)
	if !ok {
		return fmt.Errorf("%w: Delete needs exactly one equality filter on the name column", declstore.ErrUnsupportedQuery)
	}
	namespace, found, err := s.namespaceOf(ctx, name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: %s", agentstore.ErrNotFound, name)
	}
	if err := s.clientset.AuthorizationRules(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: %s", agentstore.ErrNotFound, name)
		}
		return err
	}
	s.waitForgotten(ctx, namespace, name)
	return nil
}

// Get returns the named rule, or agentstore.ErrNotFound. The row-oriented
// stores all answer a miss with that error, and authzrule.Service depends on
// it to tell "absent" (create it) from "unreadable" (report the failure).
func (s *AuthzRuleStore) Get(ctx context.Context, opts *where.Options) (*model.AuthzRuleM, error) {
	name, ok := declstore.SingleKey(opts, ruleKeyName)
	if !ok {
		return nil, fmt.Errorf("%w: Get needs exactly one equality filter on the name column", declstore.ErrUnsupportedQuery)
	}
	cr, err := s.locate(ctx, name)
	if err != nil {
		return nil, err
	}
	if cr == nil {
		return nil, fmt.Errorf("%w: %s", agentstore.ErrNotFound, name)
	}
	return authzRuleFromObject(cr)
}

// List returns every rule, ordered by name so pagination is stable. The filters
// the console's rule table asks for are honored; anything the object side
// cannot express is refused rather than silently ignored.
func (s *AuthzRuleStore) List(ctx context.Context, opts *where.Options) (int64, []*model.AuthzRuleM, error) {
	if opts != nil && (len(opts.Queries) > 0 || len(opts.Clauses) > 0) {
		return 0, nil, fmt.Errorf("%w: List cannot express raw queries or clauses", declstore.ErrUnsupportedQuery)
	}
	filters, err := ruleFilters(opts)
	if err != nil {
		return 0, nil, err
	}

	items, err := s.lister.List(labels.Everything())
	if err != nil {
		return 0, nil, fmt.Errorf("controlplane: list authorization rules: %w", err)
	}

	rows := make([]*model.AuthzRuleM, 0, len(items))
	for _, item := range items {
		row, err := authzRuleFromObject(item)
		if err != nil {
			// An object naming subjects of more than one kind has no row shape.
			// Dropping it from the list would hide a rule that is in force, so
			// it is reported with the kinds joined — visible, and obviously not
			// something the row form can round-trip.
			row = unrepresentableRow(item, err)
		}
		if !ruleMatches(row, filters) {
			continue
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	total := int64(len(rows))
	if opts != nil && opts.Offset > 0 {
		if opts.Offset >= len(rows) {
			rows = nil
		} else {
			rows = rows[opts.Offset:]
		}
	}
	if opts != nil && opts.Limit > 0 && opts.Limit < len(rows) {
		rows = rows[:opts.Limit]
	}
	return total, rows, nil
}

// locate returns the object named name, wherever it lives, or nil when there is
// none. The informer is the fast path; a miss falls back to the API server so a
// rule this process wrote a moment ago is not reported missing just because the
// watch event has not arrived yet.
func (s *AuthzRuleStore) locate(ctx context.Context, name string) (*agentv1alpha1.AuthorizationRule, error) {
	if cr := findByNamespacedName(s.lister, name); cr != nil {
		return cr, nil
	}
	// The cache missed, and the caller is usually provisioning right after a
	// write, so the API server is asked authoritatively — a cache-served answer
	// could report the object this process just created as missing.
	cr, err := s.clientset.AuthorizationRules(s.namespace).Get(ctx, name, metav1.GetOptions{}) //nolint:kubelistcheck // must not read a stale cache
	switch {
	case err == nil:
		return cr, nil
	case apierrors.IsNotFound(err):
		return nil, nil
	default:
		return nil, err
	}
}

// namespaceOf resolves the namespace the named rule lives in. An object this
// process has not seen yet can only be in the write namespace, because that is
// where this store creates them.
func (s *AuthzRuleStore) namespaceOf(ctx context.Context, name string) (string, bool, error) {
	items, err := s.lister.List(labels.Everything())
	if err != nil {
		return "", false, fmt.Errorf("controlplane: list authorization rules: %w", err)
	}
	namespaces := make([]string, 0, 1)
	for _, item := range items {
		if item.Name == name {
			namespaces = append(namespaces, item.Namespace)
		}
	}
	switch len(namespaces) {
	case 0:
		// Not in the cache yet (or at all). Probe the write namespace; this is
		// exactly the read-after-write case a cache cannot answer.
		//nolint:kubelistcheck // must not read a stale cache
		if _, err := s.clientset.AuthorizationRules(s.namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return s.namespace, true, nil
		} else if !apierrors.IsNotFound(err) {
			return "", false, err
		}
		return "", false, nil
	case 1:
		return namespaces[0], true, nil
	default:
		// The SQL table had a unique name column; the object store does not
		// enforce one across namespaces. Pick deterministically and say so,
		// rather than switching between them from call to call.
		sort.Strings(namespaces)
		slog.WarnContext(ctx, "authz: the same rule name exists in more than one namespace; using the first",
			"name", name, "namespaces", namespaces)
		return namespaces[0], true, nil
	}
}

// waitObserved waits for the informer to see the object this process just
// wrote, so the reload authzrule.Service performs right after the write
// actually picks it up. A timeout is reported, not returned: the write did
// persist, and failing it would turn a slow watch into a failed provisioning.
func (s *AuthzRuleStore) waitObserved(ctx context.Context, namespace, name string) {
	err := wait.PollUntilContextTimeout(ctx, observeInterval, observeTimeout, true,
		func(ctx context.Context) (bool, error) {
			_, err := s.lister.AuthorizationRules(namespace).Get(name)
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return err == nil, nil
		})
	if err != nil {
		slog.WarnContext(ctx, "authz: the rule was written but its watch event did not arrive in time; "+
			"the in-memory rule set may lag until the next reload",
			"name", name, "namespace", namespace, "timeout", observeTimeout, "error", err)
	}
}

// waitForgotten waits for the informer to drop the object this process just
// deleted, for the same reason waitObserved waits on the way in: a Get that
// immediately follows a Delete must answer "absent" even though the watch event
// is still in flight.
func (s *AuthzRuleStore) waitForgotten(ctx context.Context, namespace, name string) {
	err := wait.PollUntilContextTimeout(ctx, observeInterval, observeTimeout, true,
		func(ctx context.Context) (bool, error) {
			_, err := s.lister.AuthorizationRules(namespace).Get(name)
			return apierrors.IsNotFound(err), nil
		})
	if err != nil {
		slog.WarnContext(ctx, "authz: the rule was deleted but its watch event did not arrive in time",
			"name", name, "namespace", namespace, "timeout", observeTimeout, "error", err)
	}
}

// findByNamespacedName returns the object with this name from the lister, or
// nil. Rules are addressed by a name the SQL table kept unique, so the first
// match in namespace order is the one every caller means.
//
// An object that is being deleted is skipped: the cache holds it until the
// watch event arrives, and answering with it would make a Get that follows a
// Delete report a rule that no longer exists.
func findByNamespacedName(lister agentlisters.AuthorizationRuleLister, name string) *agentv1alpha1.AuthorizationRule {
	items, err := lister.List(labels.Everything())
	if err != nil {
		return nil
	}
	var found *agentv1alpha1.AuthorizationRule
	for _, item := range items {
		if item.Name != name || !item.DeletionTimestamp.IsZero() {
			continue
		}
		if found == nil || item.Namespace < found.Namespace {
			found = item
		}
	}
	return found
}

// ruleFilters extracts the equality filters a listing may push down.
func ruleFilters(opts *where.Options) (map[string]string, error) {
	if opts == nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(opts.Filters))
	for key, value := range opts.Filters {
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("%w: filter keys must be column names", declstore.ErrUnsupportedQuery)
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%w: filter %q must compare against a string", declstore.ErrUnsupportedQuery, name)
		}
		switch name {
		case "name", "subjectType", "resourceKind", "effect":
			out[name] = text
		default:
			return nil, fmt.Errorf("%w: rules cannot be filtered by %q", declstore.ErrUnsupportedQuery, name)
		}
	}
	return out, nil
}

// ruleMatches applies the filters to a row. Values are compared exactly: the
// console's table filters are equality choices, not free text.
func ruleMatches(row *model.AuthzRuleM, filters map[string]string) bool {
	for name, want := range filters {
		var got string
		switch name {
		case "name":
			got = row.Name
		case "subjectType":
			got = row.SubjectType
		case "resourceKind":
			got = row.ResourceKind
		case "effect":
			got = row.Effect
		default:
			return false
		}
		if got != want {
			return false
		}
	}
	return true
}

// errMixedSubjectKinds reports an object whose subjects are of more than one
// kind. Such an object is two rows in the policy's view (the rule loader splits
// it) and one row cannot carry both without changing what it means.
var errMixedSubjectKinds = errors.New("mix subject kinds")

// unrepresentableRow reports an object the row shape cannot express, without
// hiding it: the kinds are joined so the listing still shows the rule, and the
// row it produces will be refused by validation if anyone tries to write it
// back.
func unrepresentableRow(cr *agentv1alpha1.AuthorizationRule, cause error) *model.AuthzRuleM {
	kinds := objectSubjectKinds(cr)
	ids := make(model.StringList, 0, len(cr.Spec.Subjects))
	for _, subject := range cr.Spec.Subjects {
		ids = append(ids, subject.Name)
	}
	row := &model.AuthzRuleM{
		Name:         cr.Name,
		SubjectType:  strings.Join(kinds, ","),
		SubjectID:    ids,
		ResourceKind: cr.Spec.Resource.Kind,
		ResourceName: model.StringList(cr.Spec.Resource.Names),
		AgentNames:   model.StringList(cr.Spec.AgentNames),
		Verbs:        model.StringList(authzVerbsFromAPI(cr.Spec.Verbs)),
		Effect:       strings.ToLower(string(cr.Spec.Effect)),
		Status:       model.StatusEnabled,
		Description:  cr.Spec.Description,
		CreatedAt:    cr.CreationTimestamp.UnixMilli(),
		UpdatedAt:    cr.CreationTimestamp.UnixMilli(),
	}
	if !cr.Spec.Enabled {
		row.Status = model.StatusDisabled
	}
	slog.Warn("authz: a rule object cannot be represented as a row", "name", cr.Name, "error", cause)
	return row
}

// objectSubjectKinds returns the distinct subject kinds an object names, in a
// stable order.
func objectSubjectKinds(cr *agentv1alpha1.AuthorizationRule) []string {
	seen := make(map[string]struct{}, len(cr.Spec.Subjects))
	kinds := make([]string, 0, len(cr.Spec.Subjects))
	for _, subject := range cr.Spec.Subjects {
		kind := strings.ToLower(subject.Kind)
		if _, ok := seen[kind]; ok {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	if cr.Spec.SubjectNamespace != "" {
		if _, ok := seen["namespace"]; !ok {
			kinds = append(kinds, "namespace")
		}
	}
	sort.Strings(kinds)
	return kinds
}

// applyAuthzRule writes the row's fields onto the object. It is shared by
// Create (a fresh object) and Update (the stored object, re-read), so the two
// cannot disagree about what a row means.
//
// # Fields the object has and the row does not
//
// AgentSelector: the row's "any agent" is ["*"], which pg2crd expressed as an
// explicit empty selector. Rewriting a selector-scoped rule through the row API
// therefore turns it into a name-scoped (or unscoped) one; there is no row form
// for a label selector, and pretending otherwise would silently widen the rule.
// Owner: the object model has no owner column, so the field is accepted and not
// persisted — the same as the migration.
func applyAuthzRule(row *model.AuthzRuleM, cr *agentv1alpha1.AuthorizationRule) error {
	subjects, subjectNamespace, err := rowSubjects(row)
	if err != nil {
		return err
	}

	verbs, err := authzVerbsToAPI(row.Verbs)
	if err != nil {
		return err
	}

	resources := rowResources(row)
	if len(resources) == 0 {
		return fmt.Errorf("rule %q names no resource", row.Name)
	}
	spec := agentv1alpha1.AuthorizationRuleSpec{
		Subjects:         subjects,
		SubjectNamespace: subjectNamespace,
		Resources:        resources,
		Verbs:            verbs,
		Effect:           effectToAPI(row.Effect),
		Enabled:          row.Enabled(),
		Description:      row.Description,
	}
	// Keep the singular field populated when the policy has exactly one
	// resource, so readers that predate the list shape still see the target.
	if len(resources) == 1 {
		spec.Resource = resources[0]
	}
	if attrs := strings.TrimSpace(row.ResourceAttributes); attrs != "" {
		if !json.Valid([]byte(attrs)) {
			return fmt.Errorf("resourceAttributes of rule %q is not valid JSON", row.Name)
		}
		spec.Attributes = &runtime.RawExtension{Raw: []byte(attrs)}
	}

	agentNames := []string(row.AgentNames)
	switch {
	case len(agentNames) == 1 && agentNames[0] == anySubject:
		// The row's "any agent" is the object's explicitly empty selector. An
		// absent selector is the different statement "not agent-scoped".
		spec.AgentSelector = &metav1.LabelSelector{}
	case len(agentNames) > 0:
		spec.AgentNames = agentNames
	}

	cr.Spec = spec
	return nil
}

// rowSubjects turns the model's canonical subject list into the object's
// subjects plus its namespace-inheritance field.
//
// The plural list is authoritative when present. A namespace subject becomes
// SubjectNamespace (the object has one), and all others stay in the list. The
// legacy single-kind columns are the fallback for a caller that only filled
// them.
func rowSubjects(row *model.AuthzRuleM) ([]rbacv1.Subject, string, error) {
	// The legacy row shape wins whenever it is populated. Every caller that
	// predates the plural shape reads a row and mutates these fields; ignoring
	// their edit because the read also filled the plural mirror would write the
	// old value back. Conversion keeps the two in step for current callers, so
	// "both present" means "they agree" except in the legacy-mutation case.
	if row.SubjectType != "" {
		return rowToSubjects(row)
	}
	if len(row.Subjects) == 0 && row.SubjectNamespace == "" {
		return rowToSubjects(row)
	}

	out := make([]rbacv1.Subject, 0, len(row.Subjects))
	namespace := strings.TrimSpace(row.SubjectNamespace)
	for _, subject := range row.Subjects {
		kind := strings.ToLower(strings.TrimSpace(subject.Kind))
		name := strings.TrimSpace(subject.Name)
		switch kind {
		case "namespace":
			if name == "" {
				return nil, "", fmt.Errorf("rule %q names an empty namespace subject", row.Name)
			}
			if namespace != "" && namespace != name {
				return nil, "", fmt.Errorf(
					"rule %q inherits through both %q and %q; an object inherits through exactly one namespace",
					row.Name, namespace, name)
			}
			namespace = name
		case "*":
			out = append(out, rbacv1.Subject{Kind: rbacv1.GroupKind, Name: anySubject})
		case "user", "group", "serviceaccount":
			if name == "" {
				return nil, "", fmt.Errorf("rule %q names an empty subject", row.Name)
			}
			k := rbacv1.UserKind
			switch kind {
			case "group":
				k = rbacv1.GroupKind
			case "serviceaccount":
				k = rbacv1.ServiceAccountKind
			}
			out = append(out, rbacv1.Subject{Kind: k, Name: name, Namespace: subject.Namespace})
		default:
			return nil, "", fmt.Errorf("unknown subject type %q", subject.Kind)
		}
	}
	if len(out) == 0 && namespace == "" {
		return nil, "", fmt.Errorf("rule %q names no subject, which matches nobody", row.Name)
	}
	return out, namespace, nil
}

// rowResources returns the object's resource list. The plural field is
// authoritative when present; the legacy single-resource columns are the
// fallback for a caller that only filled them.
func rowResources(row *model.AuthzRuleM) []agentv1alpha1.AuthorizationResource {
	// The legacy field wins when populated, for the same reason as subjects: a
	// caller editing a row it read must not have its change overwritten by the
	// mirror. `NormalizeLegacyFields` clears it for multi-resource policies, so
	// those take the plural branch.
	if row.ResourceKind != "" {
		return []agentv1alpha1.AuthorizationResource{{
			Kind:  row.ResourceKind,
			Names: []string(row.ResourceName),
		}}
	}
	if len(row.Resources) == 0 {
		return nil
	}
	out := make([]agentv1alpha1.AuthorizationResource, 0, len(row.Resources))
	for _, resource := range row.Resources {
		if strings.TrimSpace(resource.Kind) == "" {
			continue
		}
		out = append(out, agentv1alpha1.AuthorizationResource{
			Kind:  resource.Kind,
			Names: append([]string(nil), resource.Names...),
		})
	}
	return out
}

// rowToSubjects turns the legacy single-kind columns into the object's
// subjects. It remains the fallback for writes that predate the plural shape.
func rowToSubjects(row *model.AuthzRuleM) ([]rbacv1.Subject, string, error) {
	ids := make([]string, 0, len(row.SubjectID))
	for _, id := range row.SubjectID {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}

	switch strings.ToLower(strings.TrimSpace(row.SubjectType)) {
	case anySubject:
		// The row's subject_type "*" matches any subject. The object has no
		// such statement; the migration expressed it as the group wildcard,
		// and the reverse mapping recognizes exactly that shape.
		return []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: anySubject}}, "", nil
	case "namespace":
		if len(ids) != 1 {
			return nil, "", fmt.Errorf(
				"rule %q inherits through %d namespaces; an object inherits through exactly one", row.Name, len(ids))
		}
		return nil, ids[0], nil
	case "user", "group", "serviceaccount":
		if len(ids) == 0 {
			return nil, "", fmt.Errorf("rule %q names no subject, which matches nobody", row.Name)
		}
		kind := rbacv1.UserKind
		switch strings.ToLower(row.SubjectType) {
		case "group":
			kind = rbacv1.GroupKind
		case "serviceaccount":
			kind = rbacv1.ServiceAccountKind
		}
		out := make([]rbacv1.Subject, 0, len(ids))
		for _, id := range ids {
			out = append(out, rbacv1.Subject{Kind: kind, Name: id})
		}
		return out, "", nil
	default:
		return nil, "", fmt.Errorf("unknown subject type %q", row.SubjectType)
	}
}

// authzRuleFromObject turns an object into the row the runtime and the admin
// API speak. It is the inverse of applyAuthzRule, and it refuses an object the
// row shape cannot hold rather than approximating it.
func authzRuleFromObject(cr *agentv1alpha1.AuthorizationRule) (*model.AuthzRuleM, error) {
	subjects := make([]model.AuthzSubject, 0, len(cr.Spec.Subjects)+1)
	for _, subject := range cr.Spec.Subjects {
		subjects = append(subjects, model.AuthzSubject{
			Kind:      strings.ToLower(subject.Kind),
			Name:      subject.Name,
			Namespace: subject.Namespace,
		})
	}
	if cr.Spec.SubjectNamespace != "" {
		subjects = append(subjects, model.AuthzSubject{
			Kind: "namespace",
			Name: cr.Spec.SubjectNamespace,
		})
	}
	if len(subjects) == 0 {
		return nil, errors.New("names no subject, which matches nobody")
	}

	resources := make([]model.AuthzResource, 0, len(cr.Spec.Resources)+1)
	for _, resource := range cr.Spec.Resources {
		if strings.TrimSpace(resource.Kind) == "" {
			continue
		}
		resources = append(resources, model.AuthzResource{
			Kind:  resource.Kind,
			Names: append([]string(nil), resource.Names...),
		})
	}
	if len(resources) == 0 && cr.Spec.Resource.Kind != "" {
		resources = append(resources, model.AuthzResource{
			Kind:  cr.Spec.Resource.Kind,
			Names: append([]string(nil), cr.Spec.Resource.Names...),
		})
	}
	if len(resources) == 0 {
		return nil, errors.New("names no resource")
	}

	row := &model.AuthzRuleM{
		Name:             cr.Name,
		Subjects:         subjects,
		Resources:        resources,
		SubjectNamespace: cr.Spec.SubjectNamespace,
		AgentNames:       model.StringList(cr.Spec.AgentNames),
		Verbs:            model.StringList(authzVerbsFromAPI(cr.Spec.Verbs)),
		Effect:           strings.ToLower(string(cr.Spec.Effect)),
		Status:           model.StatusEnabled,
		Description:      cr.Spec.Description,
		CreatedAt:        cr.CreationTimestamp.UnixMilli(),
		UpdatedAt:        cr.CreationTimestamp.UnixMilli(),
	}
	// The flat columns remain for readers and writers that only know the old
	// shape. A mixed-kind policy has no flat spelling; the plural fields are the
	// complete statement.
	row.NormalizeLegacyFields()

	if cr.Spec.Attributes != nil {
		row.ResourceAttributes = string(cr.Spec.Attributes.Raw)
	}
	if !cr.Spec.Enabled {
		row.Status = model.StatusDisabled
	}
	if cr.Spec.AgentSelector != nil && len(row.AgentNames) == 0 {
		row.AgentNames = model.StringList{anySubject}
	}
	if !cr.DeletionTimestamp.IsZero() {
		row.Status = model.StatusDisabled
	}
	return row, nil
}

// authzVerbsToAPI maps the row's two-verb vocabulary onto the API server's
// Kubernetes verbs. It mirrors hack/pg2crd's conversion, and the reverse of the
// rule source's mapVerbs, so a row written here evaluates exactly as the row it
// came from did.
func authzVerbsToAPI(verbs model.StringList) ([]string, error) {
	seen := make(map[string]struct{}, len(verbs))
	out := make([]string, 0, len(verbs))
	add := func(verb string) {
		if _, ok := seen[verb]; ok {
			return
		}
		seen[verb] = struct{}{}
		out = append(out, verb)
	}
	for _, verb := range verbs {
		switch strings.ToLower(strings.TrimSpace(verb)) {
		case "read":
			for _, v := range []string{"get", "list", "watch"} {
				add(v)
			}
		case "write":
			for _, v := range []string{"create", "update", "patch", "delete"} {
				add(v)
			}
		case "get", "list", "watch", "create", "update", "patch", "delete", "use", "*":
			add(strings.ToLower(strings.TrimSpace(verb)))
		case "":
		default:
			return nil, fmt.Errorf("unknown verb %q", verb)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("a rule needs at least one verb")
	}
	return out, nil
}

// authzVerbsFromAPI is the inverse: get|list|watch are read, everything else
// that changes state is write. It matches the rule source's mapVerbs, so the
// row the admin API shows is the row the policy evaluates.
func authzVerbsFromAPI(verbs []string) []string {
	seen := make(map[string]struct{}, len(verbs))
	out := make([]string, 0, len(verbs))
	add := func(verb string) {
		if _, ok := seen[verb]; ok {
			return
		}
		seen[verb] = struct{}{}
		out = append(out, verb)
	}
	for _, verb := range verbs {
		switch strings.ToLower(strings.TrimSpace(verb)) {
		case "get", "list", "watch":
			add("read")
		case "create", "update", "patch", "delete":
			add("write")
		case "":
		default:
			add(strings.ToLower(strings.TrimSpace(verb)))
		}
	}
	return out
}

// effectToAPI defaults an empty or unrecognized effect to Allow, the same
// default the migration applied.
func effectToAPI(effect string) agentv1alpha1.AuthorizationEffect {
	if strings.EqualFold(strings.TrimSpace(effect), string(agentv1alpha1.EffectDeny)) {
		return agentv1alpha1.EffectDeny
	}
	return agentv1alpha1.EffectAllow
}
