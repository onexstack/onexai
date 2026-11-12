package oai.authz

import rego.v1

# decision is the single query result evaluated by authz/opa.Authorizer.
# Shape: {"decision": "allow" | "deny" | "no_opinion", "reason": string}
#
# input document shape (see input.go buildRegoInput):
#   {
#     "subject": {"kind": "user"|"group"|"serviceaccount", "primary_id": "...",
#                 "group_codes": ["..."], "oncall_group_codes": ["..."],
#                 "group_namespace": "..."},
#     "verb": "read" | "write",
#     "resource_kind": "agent" | "mcp" | "tool" | "skill" | "knowledgebase" |
#                      "workspace" | "namespace" | "service" | "cluster" | ...,
#     "resource_name": "...",        // canonical "namespace/name" for refs
#     "resource_name_bare": "...",   // same name without the namespace
#     "agent_name": "...",
#     "mcp_name": "...",
#     "resource_attributes": {"env": "test", ...}  // may be nil/absent
#   }
#
# Workspace / namespace support has two independent, complementary halves —
# neither requires the other:
#
#   - Resource-side: "workspace" as resource_kind addresses an AI-resource
#     business-line workspace directly (NOT a K8s cluster namespace, which is
#     resource_kind "namespace"). No special rule is needed for this: it flows
#     through the generic resource_matches exact-match branch below like any
#     other resource_kind, fed by Coordinator.AuthorizeAgent's Workspace-level
#     check (authz.ResourceKindWorkspace).
#   - Subject-side: "namespace" as subject_type lets a rule be written once
#     against a namespace ("subject_type":"namespace", "subject_id":
#     ["team-a"]) and apply to every user currently scoped to any duty Group
#     belonging to that namespace, without enumerating those Groups' codes —
#     see the subject_matches branch below, keyed by
#     input.subject.group_namespace. This is the namespace -> Group -> User
#     inheritance step.
#
# input.subject.group_namespace is currently always empty: SubjectFromUser sets
# no group context, and nothing constructs a Group Subject yet, so that branch is
# inert until such a source lands. A namespace *is* populated — every AI
# resource model carries one and Coordinator.WithNamespaceAuthz issues the
# "use namespace:<ns>" check — so the namespace branch is live.
#
# data document shape (see store.go RuleStore.Snapshot -> rulesToRegoData):
#   data.authz.rules = [
#     {"name": "...", "subject_type": "user"|"group"|"serviceaccount"|"namespace"|"*",
#      "subject_id": [...],
#      "resource_kind": "agent"|"mcp"|"tool"|"skill"|"knowledgebase"|"workspace"|"namespace"|"*",
#      "resource_name": [...],
#      "agent_names": [...] | null,   // [] / null = not agent-scoped (see agent_matches)
#      "verbs": [...], "effect": "allow"|"deny",
#      "resource_attributes": {"env": "test"} | null | ["val1","val2"]}
#   ]
#
# subject_type="serviceaccount" needs no dedicated subject_matches branch: the
# generic branch below (matching r.subject_type against input.subject.kind,
# and sid against input.subject.primary_id) already covers it — "user" and
# "group" are not hardcoded there, so any SubjectKind ever added works through
# the same branch as long as Subject.PrimaryID() returns a stable identifier
# for it (see authz.Subject.PrimaryID's doc).

default decision := {"decision": "no_opinion", "reason": "no matching rule"}

# Explicit deny rules take priority: any matching deny rule denies outright,
# regardless of how many allow rules also match.
#
# # Why the matches are collected before the reason is formed
#
# `decision` is a *complete* rule, so each of its bodies has to produce exactly
# one output. Binding `r` to each matching rule inside the body (`some r in
# data.authz.rules`) instead makes the body satisfiable once per match, and two
# matches are then two outputs, which OPA rejects with
#
#	eval_conflict_error: complete rules must not produce multiple outputs
#
# The evaluation fails as a whole, the Union reads an execution error as
# NoOpinion (union.go), and a request that *several* rules grant is then denied
# by the fail-closed default. That is not hypothetical: any workspace covered by
# both the seeded `allow-fz-default-agent-workspace` (resource_name ["*"]) and a
# platform's own workspace grant hits it on every run, and the caller sees the
# 403 "workspace: authorization: all levels NoOpinion (fail-closed)" with no
# hint that the rules that deny them are the ones that allow them.
#
# Collecting the matching names into one sorted list first gives the body a
# single value to report however many rules matched — and naming them all is
# also the more useful reason string.
decision := {"decision": "deny", "reason": reason} if {
	deny_rules := matched_rule_names("deny")
	count(deny_rules) > 0
	reason := sprintf("matched deny rule(s) %s", [concat(", ", deny_rules)])
}

# Otherwise, allow if at least one allow rule matches.
decision := {"decision": "allow", "reason": reason} if {
	not any_deny_matches
	allow_rules := matched_rule_names("allow")
	count(allow_rules) > 0
	reason := sprintf("matched allow rule(s) %s", [concat(", ", allow_rules)])
}

any_deny_matches if {
	count(matched_rule_names("deny")) > 0
}

# matched_rule_names returns the sorted names of the rules of the given effect
# that cover the current input: the single deterministic value the complete
# rules above report.
matched_rule_names(effect) := names if {
	names := sort([name |
		some r in data.authz.rules
		r.effect == effect
		rule_matches(r)
		name := r.name
	])
}

# rule_matches reports whether AuthzRuleM record r covers the current input
# on every dimension: subject, resource, agent, verb, attributes.
rule_matches(r) if {
	subject_matches(r)
	resource_matches(r)
	agent_matches(r)
	verb_matches(r)
	resource_attrs_match(r)
}

subject_matches(r) if {
	r.subject_type == "*"
}

subject_matches(r) if {
	r.subject_type == input.subject.kind
	some sid in r.subject_id
	sid == "*"
}

# Generic exact-match branch, not specific to any one SubjectKind: this is
# what makes subject_type="serviceaccount" (and any future SubjectKind) work
# with zero additional rules — see the package doc above.
subject_matches(r) if {
	r.subject_type == input.subject.kind
	some sid in r.subject_id
	sid == input.subject.primary_id
}

# A user also carries the grants of any group rule whose subject_id lists a
# group the user currently belongs to (input.subject.group_codes, populated by
# the caller from console-managed user groups and on-call duty groups). Group
# membership does NOT flow the other direction: a group Subject does not pick up
# grants scoped to its individual members.
subject_matches(r) if {
	r.subject_type == "group"
	input.subject.kind == "user"
	some sid in r.subject_id
	some gc in input.subject.group_codes
	sid == gc
}

# A user also carries the grants of any namespace-scoped rule whose
# subject_id lists the namespace the user's current duty group belongs to
# (input.subject.group_namespace). This is the namespace -> Group -> User
# inheritance step: rules can be written once against a namespace
# ("subject_type":"namespace", "subject_id":["team-a"]) without needing to
# enumerate every Group that namespace currently contains.
subject_matches(r) if {
	r.subject_type == "namespace"
	input.subject.group_namespace != ""
	some sid in r.subject_id
	sid == input.subject.group_namespace
}

resource_matches(r) if {
	r.resource_kind == "*"
}

resource_matches(r) if {
	r.resource_kind == input.resource_kind
	some rn in r.resource_name
	rn == "*"
}

resource_matches(r) if {
	r.resource_kind == input.resource_kind
	some rn in r.resource_name
	rn == input.resource_name
}

# A rule entry without a namespace qualifier matches the resource's bare name
# in any namespace. This is the historical contract: namespaced references are
# addressed at runtime as canonical "namespace/name", but the seed and OneX's
# reconcile path name the object alone ("onexai", "onex-console"). Without this
# branch those rules are all-NoOpinion on every canonical reference, which the
# Union turns into a fail-closed Deny.
#
# The compatibility is one-directional, and the direction is what keeps a
# canonical rule precise: a qualified rule entry only ever matches the exact
# canonical resource_name above, and a bare request never picks up a qualified
# rule. Deny rules go through the same match, so a bare deny is namespace-wide —
# which is the safe direction.
resource_matches(r) if {
	r.resource_kind == input.resource_kind
	some rn in r.resource_name
	input.resource_name_bare != ""
	rn == input.resource_name_bare
}

verb_matches(r) if {
	some v in r.verbs
	v == input.verb
}

# agent_matches reports whether the rule's Agent scope covers the Agent the
# request names.
#
# # A rule that names no Agent matches every request
#
# That is not a default so much as the absence of the dimension, and it is what
# keeps every rule written before the column existed meaning exactly what it
# meant: the rule set has always answered per subject and resource, never per
# Agent. Opting *into* the dimension is how a grant says "…and only when running
# this Agent".
#
# # Why it is an allow-as-well-as-deny dimension
#
# Both directions matter, and the deny is the load-bearing one. Every dimension
# — including resource references — is fail-closed, so an MCP no rule mentions
# is refused rather than permitted; the allow is what makes an Agent able to use
# its own references at all. The deny is what keeps one Agent away from a
# resource another Agent may use: the seed's deny-fz-public-mcp is written that
# way.
#
# input.agent_names is the run's whole selection. A Tool is reachable only
# through some selected Agent, so a rule scoped to any one of them applies: a
# merged run must not escape an agent-scoped deny by also selecting an Agent the
# rule does not name. input.agent_name is the single-Agent form (and the display
# name in reason strings); the two agree for single-Agent runs, which is what
# the chat service schedules.
agent_matches(r) if {
	not rule_has_agent_scope(r)
}

agent_matches(r) if {
	rule_has_agent_scope(r)
	some name in r.agent_names
	some requested in input.agent_names
	requested == name
}

# rule_has_agent_scope is true when the rule carries a non-empty Agent scope.
# r.agent_names truthiness handles absent/null/undefined; count > 0 handles [].
rule_has_agent_scope(r) if {
	r.agent_names
	count(r.agent_names) > 0
}

# resource_attrs_match reports whether the rule's resource_attributes filter (if
# any) is a subset of the input's resource_attributes. An absent, null, or empty
# resource_attributes on the rule matches everything — backward compatible
# with all existing rules that predate this feature.
resource_attrs_match(r) if {
	not rule_has_attrs(r)
}

resource_attrs_match(r) if {
	rule_has_attrs(r)
	not any_resource_attr_mismatch(r)
}

# rule_has_attrs is true when the rule carries a non-empty resource_attributes
# filter. r.resource_attributes truthiness check handles absent/null/undefined;
# count > 0 handles {}.
rule_has_attrs(r) if {
	r.resource_attributes
	count(r.resource_attributes) > 0
}

# any_resource_attr_mismatch fires when any key in the rule's resource_attributes
# either does not exist in input.resource_attributes, or none of its allowed
# values match the input value.
# Each value in r.resource_attributes[key] may be:
#   - a string → matched as exact or glob (backward compatible)
#   - an array of strings → matched if any element matches (OR semantics)
any_resource_attr_mismatch(r) if {
	some key
	r.resource_attributes[key]
	not attr_value_matches(r, key)
}

# attr_value_matches normalizes the rule's value to a flat list, then checks
# whether at least one candidate matches the input value.
attr_value_matches(r, key) if {
	candidates := normalize_to_list(r.resource_attributes[key])
	input.resource_attributes[key]
	some candidate in candidates
	candidate_matches_input(candidate, input.resource_attributes[key])
}

# normalize_to_list coerces a single string or an array into a flat []string.
# type_name(val) == "string" detects a plain string; type_name(val) == "array"
# detects an array (e.g. from JSON ["a","b"]). Both are native Rego built-ins.
normalize_to_list(val) := [c] if {
	type_name(val) == "string"
	c := val
}

normalize_to_list(val) := result if {
	type_name(val) == "array"
	result := val
}

# candidate_matches_input: exact match when no wildcards, glob.match otherwise.
# ":" is used as the glob delimiter so dots, slashes, hyphens are NOT treated
# as separators in cluster/account names.
candidate_matches_input(pattern, input_val) if {
	is_glob(pattern)
	glob.match(pattern, [":"], input_val)
}

candidate_matches_input(pattern, input_val) if {
	not is_glob(pattern)
	pattern == input_val
}

# is_glob reports whether the pattern string contains glob metacharacters.
is_glob(s) if {
	contains(s, "*")
}

is_glob(s) if {
	contains(s, "?")
}
