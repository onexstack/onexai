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

// Command pg2crd turns the rows the services used to read from PostgreSQL into
// custom resources, as YAML a person can read before creating them.
//
// DutyGroup is historical: duty groups are now a chat-owned imperative resource
// stored in PostgreSQL. Do not run the DutyGroup leg against a declserver that
// has disabled the resource; use hack/crd2pg for the opposite direction.
//
// Usage:
//
//	hack/pg2crd --config ~/.onexai/oai-agent-apiserver.yaml \
//	            --kubeconfig ~/.onexai/declserver.kubeconfig \
//	            --out /tmp/pg2crd
//	kubectl --kubeconfig ~/.onexai/declserver.kubeconfig create -f /tmp/pg2crd
//
// The conversion is not this program's own idea of the mapping. Every resource
// goes through the same `XxxToCR` function the runtime's write path uses (they
// are exported for exactly this), so a migrated object and an object the admin
// API wrote are indistinguishable — which is the property that makes the
// migration reviewable: read one document and you have read them all.
//
// Credentials the rows carried (an MCP server's headers, a model's API key) come
// out as Secrets in the same directory, because the resource references them by
// name and a reference to a Secret nobody created is a resource that never
// becomes ready. That file is a credential file: create it, then delete it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/viper"
	"gorm.io/gorm"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	agentcps "github.com/onexstack/onexai/internal/agent/store/controlplanestore"
	chatcps "github.com/onexstack/onexai/internal/chat/store/controlplanestore"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
	customoptions "github.com/onexstack/onexai/pkg/options"

	chatmodel "github.com/onexstack/onexai/internal/chat/model"

	agentmodel "github.com/onexstack/onexai/internal/agent/model"
)

func main() {
	var (
		configPath = flag.String("config", "", "Path to the service config the coredb block is read from (required)")
		kubeconfig = flag.String("kubeconfig", "", "Control-plane kubeconfig; when set, objects that already exist are skipped")
		// The namespace is not the destination, only the fallback: every object
		// lands in its namespace, or in the namespace of the agents that
		// reference it (see placement.go). The default is the namespace a row
		// with no namespace at all means (model.DefaultNamespace).
		namespace = flag.String("namespace", agentmodel.DefaultNamespace, "Namespace for objects that record no namespace and that nothing references")
		outDir    = flag.String("out", "_output/pg2crd", "Directory the manifests are written to")
	)
	flag.Parse()

	if *configPath == "" {
		fatalf("--config is required: it names the file whose coredb block holds the database to read")
	}
	if err := run(*configPath, *kubeconfig, *namespace, *outDir); err != nil {
		fatalf("%v", err)
	}
}

func run(configPath, kubeconfig, fallback, outDir string) error {
	db, err := openDB(configPath)
	if err != nil {
		return err
	}

	// Where each object goes is decided before anything is converted: it needs
	// the whole reference graph, and a conflict has to stop the migration before
	// it writes files that look complete.
	place, err := planPlacement(context.Background(), db, fallback)
	if err != nil {
		return err
	}
	place.report()

	var existing *existingSet
	if kubeconfig != "" {
		if existing, err = newExistingSet(kubeconfig); err != nil {
			return err
		}
	}

	out := &emitter{dir: outDir}
	// The workspaces come first: `kubectl create` fails on a resource whose
	// namespace does not exist, and the control plane only creates its own.
	for _, ns := range place.namespaces() {
		out.add("00-namespaces.yaml", "Namespace", "", &corev1.Namespace{
			TypeMeta:   typeMeta("core", "Namespace"),
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		}, nil)
	}
	if err := migrate(context.Background(), db, place, existing, out); err != nil {
		return err
	}
	return out.write()
}

// openDB reads the coredb block of a service config and opens it.
//
// The file is the one the service itself reads, so the database this migrates
// from is the database that service has been using — not a guess at a DSN.
func openDB(path string) (*gorm.DB, error) {
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg struct {
		CoreDB *customoptions.CoreDBOptions `mapstructure:"coredb"`
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("decode the coredb block of %s: %w", path, err)
	}
	if cfg.CoreDB == nil {
		return nil, fmt.Errorf("%s has no coredb block", path)
	}
	return cfg.CoreDB.NewDB()
}

// existingSet answers "is this object already in the control plane?" so a second
// run of the migration does not have to fail on every name it wrote the first
// time. It reads each kind once rather than issuing a GET per object.
//
// Keys are namespace-qualified: the same name can legitimately exist in two
// workspaces, and "already exists" is only true of the one it would overwrite.
type existingSet struct {
	agent map[string]map[string]struct{}
	chat  map[string]map[string]struct{}
}

func newExistingSet(kubeconfig string) (*existingSet, error) {
	// All namespaces: the migration writes into each object's workspace, so a
	// listing scoped to one of them would miss the rest and re-create objects
	// that are already there.
	client, err := declclient.New(kubeconfig, metav1.NamespaceAll, 0)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	set := &existingSet{agent: map[string]map[string]struct{}{}, chat: map[string]map[string]struct{}{}}
	all := metav1.NamespaceAll

	collect := func(target map[string]map[string]struct{}, kind string, keys []string) {
		m := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			m[key] = struct{}{}
		}
		target[kind] = m
	}

	agents, err := client.Clientset.AgentV1alpha1().Agents(all).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	collect(set.agent, kindAgent, keysOf(agents.Items, func(i *agentv1alpha1.Agent) string { return i.Namespace + "/" + i.Name }))

	for kind, list := range map[string]func() ([]string, error){
		kindPrompt: func() ([]string, error) {
			items, err := client.Clientset.AgentV1alpha1().Prompts(all).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return keysOf(items.Items, func(i *agentv1alpha1.Prompt) string { return i.Namespace + "/" + i.Name }), nil
		},
		kindSkill: func() ([]string, error) {
			items, err := client.Clientset.AgentV1alpha1().Skills(all).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return keysOf(items.Items, func(i *agentv1alpha1.Skill) string { return i.Namespace + "/" + i.Name }), nil
		},
		kindKnowledgeBase: func() ([]string, error) {
			items, err := client.Clientset.AgentV1alpha1().KnowledgeBases(all).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return keysOf(items.Items, func(i *agentv1alpha1.KnowledgeBase) string { return i.Namespace + "/" + i.Name }), nil
		},
		kindMCPServer: func() ([]string, error) {
			items, err := client.Clientset.AgentV1alpha1().MCPServers(all).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return keysOf(items.Items, func(i *agentv1alpha1.MCPServer) string { return i.Namespace + "/" + i.Name }), nil
		},
		kindModel: func() ([]string, error) {
			items, err := client.Clientset.AgentV1alpha1().Models(all).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return keysOf(items.Items, func(i *agentv1alpha1.Model) string { return i.Namespace + "/" + i.Name }), nil
		},
		kindWebhook: func() ([]string, error) {
			items, err := client.Clientset.AgentV1alpha1().Webhooks(all).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return keysOf(items.Items, func(i *agentv1alpha1.Webhook) string { return i.Namespace + "/" + i.Name }), nil
		},
		kindAuthorizationRule: func() ([]string, error) {
			items, err := client.Clientset.AgentV1alpha1().AuthorizationRules(all).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return keysOf(items.Items, func(i *agentv1alpha1.AuthorizationRule) string { return i.Namespace + "/" + i.Name }), nil
		},
		// Command grants live in the agent group now, though their legacy rows
		// are the chat service's (chat.agent_command). Listing them here keeps a
		// second run from rewriting objects the first one created.
		kindAgentBinding: func() ([]string, error) {
			items, err := client.Clientset.AgentV1alpha1().AgentBindings(all).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return keysOf(items.Items, func(i *agentv1alpha1.AgentBinding) string { return i.Namespace + "/" + i.Name }), nil
		},
	} {
		list, err := list()
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", kind, err)
		}
		collect(set.agent, kind, list)
	}

	for kind, list := range map[string]func() ([]string, error){
		kindDutyGroup: func() ([]string, error) {
			items, err := client.Clientset.ChatV1alpha1().DutyGroups(all).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return keysOf(items.Items, func(i *chatv1alpha1.DutyGroup) string { return i.Namespace + "/" + i.Name }), nil
		},
		"PeerBinding": func() ([]string, error) {
			items, err := client.Clientset.ChatV1alpha1().PeerBindings(all).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return keysOf(items.Items, func(i *chatv1alpha1.PeerBinding) string { return i.Namespace + "/" + i.Name }), nil
		},
	} {
		list, err := list()
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", kind, err)
		}
		collect(set.chat, kind, list)
	}

	return set, nil
}

func namesOf[T any](items []T, name func(*T) string) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, name(&items[i]))
	}
	return out
}

func keysOf[T any](items []T, key func(*T) string) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, key(&items[i]))
	}
	return out
}

// exists reports whether the object is already in the control plane, in the
// namespace the migration would write it to.
func (s *existingSet) exists(group, kind, namespace, name string) bool {
	if s == nil {
		return false
	}
	set := s.agent
	if group == "chat" {
		set = s.chat
	}
	_, ok := set[kind][namespace+"/"+name]
	return ok
}

// migrate reads every table and hands the converted objects to the emitter.
//
// The order is the order the manifests are created in: the Secrets the resources
// reference, then the resources themselves, then the policy that names them.
func migrate(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	// The order is the order the manifests are created in: the Secrets the
	// resources reference, then the resources themselves, then the policy that
	// names them.
	steps := []func() error{
		func() error { return migrateMCPSecrets(ctx, db, place, existing, out) },
		func() error { return migrateModels(ctx, db, place, existing, out) },
		func() error { return migratePrompts(ctx, db, place, existing, out) },
		func() error { return migrateSkills(ctx, db, place, existing, out) },
		func() error { return migrateKnowledgeBases(ctx, db, place, existing, out) },
		func() error { return migrateMCPServers(ctx, db, place, existing, out) },
		func() error { return migrateWebhooks(ctx, db, place, existing, out) },
		func() error { return migrateAgents(ctx, db, place, existing, out) },
		func() error { return migrateAuthorizationRules(ctx, db, place, existing, out) },
		// Command grants: chat.agent_command → AgentBinding. Must run after the
		// agents exist, because a binding names them.
		func() error { return migrateAgentBindings(ctx, db, place, existing, out) },
		func() error { return migrateDutyGroups(ctx, db, place, existing, out) },
		func() error { return migratePeerBindings(ctx, db, place, existing, out) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func find[T any](ctx context.Context, db *gorm.DB) ([]T, error) {
	var rows []T
	if err := db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// findLegacyAgents reads the agent rows whether or not the legacy sys_prompt
// column is still present. The column was dropped from migrated databases
// (hack/drop-agent-legacy-columns.sql); against those, SysPrompt stays empty.
func findLegacyAgents(ctx context.Context, db *gorm.DB) ([]legacyAgentRow, error) {
	if db.Migrator().HasColumn(&agentmodel.AgentM{}, "sys_prompt") {
		return find[legacyAgentRow](ctx, db)
	}
	plain, err := find[agentmodel.AgentM](ctx, db)
	if err != nil {
		return nil, err
	}
	rows := make([]legacyAgentRow, len(plain))
	for i := range plain {
		rows[i].AgentM = plain[i]
	}
	return rows, nil
}

// legacyAgentRow reads the agent table plus the sys_prompt column the running
// service no longer maps. The migration is that column's only reader: a name
// becomes a promptRef in the Agent spec, prose becomes an inline-<agent>
// Prompt, and the built-in sentinel produces neither.
type legacyAgentRow struct {
	agentmodel.AgentM
	SysPrompt string `gorm:"column:sys_prompt"`
}

// TableName keeps the embedded model's table: this is a read-only view of the
// legacy row, not a second model.
func (legacyAgentRow) TableName() string { return agentmodel.AgentM{}.TableName() }

// legacyLabels reads the tags column the rows still carry, as the label set it
// always meant.
//
// The column is history. The store stopped mapping it when labels became the
// object's own metadata, so the migration is the one reader left — and the one
// place where "team=sre" has to be understood as a label.
func legacyLabels(ctx context.Context, db *gorm.DB, model any) (map[string]agentmodel.StringMap, error) {
	if !db.Migrator().HasColumn(model, "tags") {
		return map[string]agentmodel.StringMap{}, nil
	}

	var rows []struct {
		Name string
		Tags string
	}
	if err := db.WithContext(ctx).Model(model).Select("name, tags").Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := make(map[string]agentmodel.StringMap, len(rows))
	for _, row := range rows {
		var tags agentmodel.StringList
		if err := tags.Scan(row.Tags); err != nil {
			return nil, fmt.Errorf("decoding the tags of %q: %w", row.Name, err)
		}
		out[row.Name] = agentcps.LabelsFromTags(tags)
	}
	return out, nil
}

func objectMeta(name, namespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: namespace}
}

// typeMeta is what makes the document a Kubernetes object: without apiVersion
// and kind, `kubectl create -f` has nothing to decode it as.
func typeMeta(group, kind string) metav1.TypeMeta {
	if group == "core" {
		return metav1.TypeMeta{APIVersion: "v1", Kind: kind}
	}
	return metav1.TypeMeta{APIVersion: group + ".onexai.io/v1alpha1", Kind: kind}
}

func migrateAgents(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	rows, err := findLegacyAgents(ctx, db)
	if err != nil {
		return fmt.Errorf("read agents: %w", err)
	}
	labels, err := legacyLabels(ctx, db, &agentmodel.AgentM{})
	if err != nil {
		return fmt.Errorf("read agent labels: %w", err)
	}
	// The flag only means something while the column exists; on a migrated
	// database (column dropped) it must stay false so the conversion does not
	// write a legacy sysPrompt back.
	hasSysPrompt := db.Migrator().HasColumn(&agentmodel.AgentM{}, "sys_prompt")
	for i := range rows {
		row := &rows[i]
		ns := place.namespaceOf(kindAgent, row.Name)
		row.Labels = labels[row.Name]
		obj := &agentv1alpha1.Agent{TypeMeta: typeMeta("agent", "Agent"), ObjectMeta: objectMeta(row.Name, ns)}
		inline := isInlinePromptValue(row.SysPrompt)
		if !inline && row.SysPrompt != "" && row.SysPrompt != agentv1alpha1.BuiltinPrompt {
			row.PromptRefs = agentmodel.StringList{row.SysPrompt}
		} else {
			row.PromptRefs = nil
		}
		agentcps.AgentToCR(&row.AgentM, obj)
		// The legacy column is a single name. Keep the migration's output on
		// spec.promptRef, exactly as it was before the list existed.
		if hasSysPrompt {
			obj.Spec.Prompts = nil
			if len(row.PromptRefs) == 1 {
				obj.Spec.PromptRef = &agentv1alpha1.Reference{Name: row.PromptRefs[0]}
			}
		}

		// Inline prompts are removed from the Agent model. A row that carried
		// one becomes a Prompt object named inline-<agent>, bound like any
		// other source. Without an owner UID in a static manifest the Prompt is
		// unowned; the controller aggregates it as-is, and reclaims it (through
		// the inline-for annotation) once the agent stops binding it.
		if inline {
			name := agentv1alpha1.InlinePromptName(row.Name)
			promptNS := place.namespaceOf(kindPrompt, name)
			prompt := &agentv1alpha1.Prompt{
				TypeMeta:   typeMeta("agent", "Prompt"),
				ObjectMeta: objectMeta(name, promptNS),
				Spec: agentv1alpha1.PromptSpec{
					CommonSpec: agentv1alpha1.CommonSpec{
						Enabled:     true,
						DisplayName: "Inline prompt for " + row.Name,
						Description: "Migrated from the agent's inline prompt.",
					},
					Template: row.SysPrompt,
				},
			}
			prompt.Annotations = map[string]string{agentv1alpha1.AnnotationInlineFor: row.Name}
			out.add("10-prompts.yaml", kindPrompt, promptNS, prompt, existing)
			if !hasAgentPromptRef(obj.Spec.Prompts, name) {
				obj.Spec.Prompts = append(obj.Spec.Prompts, agentv1alpha1.Reference{Name: name})
			}
		}

		out.add("20-agents.yaml", kindAgent, ns, obj, existing)
	}
	return nil
}

// isInlinePromptValue reports whether a legacy SysPrompt value is prose rather
// than a Prompt name or the built-in sentinel. It mirrors the runtime store's
// heuristic.
func isInlinePromptValue(value string) bool {
	if value == "" || value == agentv1alpha1.BuiltinPrompt {
		return false
	}
	if len(value) > 253 || strings.ContainsAny(value, " \n\t") {
		return true
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_') {
			return true
		}
	}
	return false
}

func hasAgentPromptRef(refs []agentv1alpha1.Reference, name string) bool {
	for _, ref := range refs {
		if ref.Name == name && ref.Namespace == "" {
			return true
		}
	}
	return false
}

// simpleMigration is the table-and-converter pair for the resources whose
// migration is the same read-table, read-labels, convert, emit loop.
type simpleMigration[M any, CR metav1.Object] struct {
	File       string
	Kind       string
	ReadError  string
	LabelError string
	// NameOf is the object name. It differs from the row's name only for a row
	// whose identity is a triple (PeerBinding).
	NameOf func(*M) string
	// LabelModel is the table legacyLabels inspects; SetLabels writes the result
	// onto the row. Both nil for a table with no tags column.
	LabelModel any
	SetLabels  func(*M, agentmodel.StringMap)
	NewCR      func(name, namespace string) CR
	Convert    func(*M, CR)
	// Before emits anything the object references (a Secret) before the object.
	Before func(row *M, obj CR, namespace string) error
}

func migrateSimple[M any, CR metav1.Object](
	ctx context.Context, db *gorm.DB, place *placement, existing *existingSet,
	out *emitter, spec simpleMigration[M, CR],
) error {
	rows, err := find[M](ctx, db)
	if err != nil {
		return fmt.Errorf("%s: %w", spec.ReadError, err)
	}

	var labels map[string]agentmodel.StringMap
	if spec.SetLabels != nil {
		labels, err = legacyLabels(ctx, db, spec.LabelModel)
		if err != nil {
			return fmt.Errorf("%s: %w", spec.LabelError, err)
		}
	}

	for i := range rows {
		row := &rows[i]
		name := spec.NameOf(row)
		namespace := place.namespaceOf(spec.Kind, name)
		if spec.SetLabels != nil {
			spec.SetLabels(row, labels[name])
		}
		obj := spec.NewCR(name, namespace)
		spec.Convert(row, obj)
		if spec.Before != nil {
			if err := spec.Before(row, obj, namespace); err != nil {
				return err
			}
		}
		out.add(spec.File, spec.Kind, namespace, obj, existing)
	}
	return nil
}

func migratePrompts(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	return migrateSimple(ctx, db, place, existing, out, simpleMigration[agentmodel.PromptM, *agentv1alpha1.Prompt]{
		File: "10-prompts.yaml", Kind: kindPrompt,
		ReadError: "read prompts", LabelError: "read prompt labels",
		NameOf:     func(row *agentmodel.PromptM) string { return row.Name },
		LabelModel: &agentmodel.PromptM{},
		SetLabels:  func(row *agentmodel.PromptM, labels agentmodel.StringMap) { row.Labels = labels },
		NewCR: func(name, namespace string) *agentv1alpha1.Prompt {
			return &agentv1alpha1.Prompt{TypeMeta: typeMeta("agent", "Prompt"), ObjectMeta: objectMeta(name, namespace)}
		},
		Convert: agentcps.PromptToCR,
	})
}

func migrateSkills(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	return migrateSimple(ctx, db, place, existing, out, simpleMigration[agentmodel.SkillM, *agentv1alpha1.Skill]{
		File: "11-skills.yaml", Kind: kindSkill,
		ReadError: "read skills", LabelError: "read skill labels",
		NameOf:     func(row *agentmodel.SkillM) string { return row.Name },
		LabelModel: &agentmodel.SkillM{},
		SetLabels:  func(row *agentmodel.SkillM, labels agentmodel.StringMap) { row.Labels = labels },
		NewCR: func(name, namespace string) *agentv1alpha1.Skill {
			return &agentv1alpha1.Skill{TypeMeta: typeMeta("agent", "Skill"), ObjectMeta: objectMeta(name, namespace)}
		},
		Convert: agentcps.SkillToCR,
	})
}

func migrateKnowledgeBases(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	return migrateSimple(ctx, db, place, existing, out, simpleMigration[agentmodel.KnowledgeBaseM, *agentv1alpha1.KnowledgeBase]{
		File: "12-knowledgebases.yaml", Kind: kindKnowledgeBase,
		ReadError: "read knowledge bases", LabelError: "read knowledge base labels",
		NameOf:     func(row *agentmodel.KnowledgeBaseM) string { return row.Name },
		LabelModel: &agentmodel.KnowledgeBaseM{},
		SetLabels:  func(row *agentmodel.KnowledgeBaseM, labels agentmodel.StringMap) { row.Labels = labels },
		NewCR: func(name, namespace string) *agentv1alpha1.KnowledgeBase {
			return &agentv1alpha1.KnowledgeBase{TypeMeta: typeMeta("agent", "KnowledgeBase"), ObjectMeta: objectMeta(name, namespace)}
		},
		Convert: agentcps.KnowledgeBaseToCR,
	})
}

func migrateMCPServers(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	return migrateSimple(ctx, db, place, existing, out, simpleMigration[agentmodel.MCPM, *agentv1alpha1.MCPServer]{
		File: "13-mcpservers.yaml", Kind: kindMCPServer,
		ReadError: "read mcp servers", LabelError: "read mcp server labels",
		NameOf:     func(row *agentmodel.MCPM) string { return row.Name },
		LabelModel: &agentmodel.MCPM{},
		SetLabels:  func(row *agentmodel.MCPM, labels agentmodel.StringMap) { row.Labels = labels },
		NewCR: func(name, namespace string) *agentv1alpha1.MCPServer {
			return &agentv1alpha1.MCPServer{TypeMeta: typeMeta("agent", "MCPServer"), ObjectMeta: objectMeta(name, namespace)}
		},
		Convert: agentcps.MCPToCR,
	})
}

func migrateWebhooks(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	return migrateSimple(ctx, db, place, existing, out, simpleMigration[agentmodel.WebhookM, *agentv1alpha1.Webhook]{
		File: "15-webhooks.yaml", Kind: kindWebhook,
		ReadError: "read webhooks",
		NameOf:    func(row *agentmodel.WebhookM) string { return row.Name },
		NewCR: func(name, namespace string) *agentv1alpha1.Webhook {
			return &agentv1alpha1.Webhook{TypeMeta: typeMeta("agent", "Webhook"), ObjectMeta: objectMeta(name, namespace)}
		},
		Convert: agentcps.WebhookToCR,
	})
}

// migrateMCPSecrets writes the Secret each MCP server's headers become.
//
// The server references it by name (spec.headersRef), and an object whose
// reference names a Secret nobody created never becomes ready — so the Secret is
// part of the migration, not an afterthought.
func migrateMCPSecrets(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	rows, err := find[agentmodel.MCPM](ctx, db)
	if err != nil {
		return fmt.Errorf("read mcp servers: %w", err)
	}
	for i := range rows {
		row := &rows[i]
		// The Secret lives where the server that references it lives: a
		// headersRef only resolves inside its own namespace.
		ns := place.namespaceOf(kindMCPServer, row.Name)
		headers, err := agentcps.DecodeHeaders(row.Headers)
		if err != nil {
			return fmt.Errorf("mcp server %q has headers that are not a JSON object: %w", row.Name, err)
		}
		if len(headers) == 0 {
			continue
		}
		// The name comes from the converter, so the Secret and the reference
		// cannot disagree about it.
		obj := &agentv1alpha1.MCPServer{TypeMeta: typeMeta("agent", "MCPServer"), ObjectMeta: objectMeta(row.Name, ns)}
		agentcps.MCPToCR(row, obj)
		if obj.Spec.HeadersRef == nil {
			continue
		}
		data := make(map[string][]byte, len(headers))
		for name, value := range headers {
			data[name] = []byte(value)
		}
		out.add("00-secrets.yaml", "Secret", ns, &corev1.Secret{
			TypeMeta:   typeMeta("core", "Secret"),
			ObjectMeta: objectMeta(obj.Spec.HeadersRef.Name, ns),
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}, nil)
	}
	return nil
}

func migrateModels(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	return migrateSimple(ctx, db, place, existing, out, simpleMigration[agentmodel.ModelM, *agentv1alpha1.Model]{
		File: "14-models.yaml", Kind: kindModel,
		ReadError: "read models", LabelError: "read model labels",
		NameOf:     func(row *agentmodel.ModelM) string { return row.Name },
		LabelModel: &agentmodel.ModelM{},
		SetLabels:  func(row *agentmodel.ModelM, labels agentmodel.StringMap) { row.Labels = labels },
		NewCR: func(name, namespace string) *agentv1alpha1.Model {
			return &agentv1alpha1.Model{TypeMeta: typeMeta("agent", "Model"), ObjectMeta: objectMeta(name, namespace)}
		},
		Convert: agentcps.ModelToCR,
		Before: func(row *agentmodel.ModelM, obj *agentv1alpha1.Model, namespace string) error {
			if row.APIKey != "" && obj.Spec.CredentialRef != nil {
				out.add("00-secrets.yaml", "Secret", namespace, &corev1.Secret{
					TypeMeta:   typeMeta("core", "Secret"),
					ObjectMeta: objectMeta(obj.Spec.CredentialRef.Name, namespace),
					Type:       corev1.SecretTypeOpaque,
					Data:       map[string][]byte{obj.Spec.CredentialRef.Key: []byte(row.APIKey)},
				}, nil)
			}
			return nil
		},
	})
}

func migrateDutyGroups(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	return migrateSimple(ctx, db, place, existing, out, simpleMigration[chatmodel.OncallGroupM, *chatv1alpha1.DutyGroup]{
		File: "41-dutygroups.yaml", Kind: kindDutyGroup,
		ReadError: "read duty groups",
		NameOf:    func(row *chatmodel.OncallGroupM) string { return row.Name },
		NewCR: func(name, namespace string) *chatv1alpha1.DutyGroup {
			return &chatv1alpha1.DutyGroup{TypeMeta: typeMeta("chat", "DutyGroup"), ObjectMeta: objectMeta(name, namespace)}
		},
		Convert: chatcps.DutyGroupToCR,
	})
}

func migratePeerBindings(ctx context.Context, db *gorm.DB, place *placement, existing *existingSet, out *emitter) error {
	return migrateSimple(ctx, db, place, existing, out, simpleMigration[chatmodel.PeerAgentM, *chatv1alpha1.PeerBinding]{
		File: "42-peerbindings.yaml", Kind: kindPeerBinding,
		ReadError: "read peer bindings",
		// The row has no name column: the object's name is derived from the
		// triple it is keyed by, exactly as the store derives it.
		NameOf: func(row *chatmodel.PeerAgentM) string {
			return chatcps.BindingName(row.Backend, row.Scope, row.PeerID)
		},
		NewCR: func(name, namespace string) *chatv1alpha1.PeerBinding {
			return &chatv1alpha1.PeerBinding{TypeMeta: typeMeta("chat", "PeerBinding"), ObjectMeta: objectMeta(name, namespace)}
		},
		Convert: chatcps.PeerBindingToCR,
	})
}

// emitter collects documents per file and writes them out.
type emitter struct {
	dir   string
	files []*outFile
}

type outFile struct {
	name string
	docs []any
}

// add queues one document, returning whether it was queued (false when the
// control plane already has the object).
func (e *emitter) add(file, kind, namespace string, obj any, existing *existingSet) bool {
	name := objectName(obj)
	group := "agent"
	switch obj.(type) {
	case *chatv1alpha1.DutyGroup, *chatv1alpha1.PeerBinding:
		group = "chat"
	}
	if existing.exists(group, kind, namespace, name) {
		fmt.Fprintf(stderr, "skip %s %s/%s: already in the control plane\n", kind, namespace, name)
		return false
	}

	for _, f := range e.files {
		if f.name == file {
			f.docs = append(f.docs, obj)
			return true
		}
	}
	e.files = append(e.files, &outFile{name: file, docs: []any{obj}})
	return true
}

func (e *emitter) write() error {
	if len(e.files) == 0 {
		fmt.Println("nothing to migrate: the tables hold no rows")
		return nil
	}
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		return err
	}

	sort.SliceStable(e.files, func(i, j int) bool { return e.files[i].name < e.files[j].name })

	total := 0
	for _, f := range e.files {
		var buf bytes.Buffer
		for _, doc := range f.docs {
			body, err := manifestOf(doc)
			if err != nil {
				return fmt.Errorf("encode a document for %s: %w", f.name, err)
			}
			buf.WriteString("---\n")
			buf.Write(body)
		}
		path := filepath.Join(e.dir, f.name)
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			return err
		}
		fmt.Printf("%-28s %3d document(s)  %s\n", f.name, len(f.docs), path)
		total += len(f.docs)
	}

	fmt.Printf("\n%d document(s) in %s\n", total, e.dir)
	fmt.Printf("create them with:\n  kubectl --kubeconfig ~/.onexai/declserver.kubeconfig create -f %s\n", e.dir)
	if secrets, ok := e.byName("00-secrets.yaml"); ok && len(secrets.docs) > 0 {
		fmt.Printf("\n%s holds credentials carried over from the database: create it, then delete the file.\n",
			filepath.Join(e.dir, secrets.name))
	}
	return nil
}

func (e *emitter) byName(name string) (*outFile, bool) {
	for _, f := range e.files {
		if f.name == name {
			return f, true
		}
	}
	return nil, false
}

// manifestOf renders one object, without its status.
//
// Status is the control loop's half of the object: the migration has nothing
// true to say about it, and a document that carried an empty one would read as
// though it did.
func manifestOf(doc any) ([]byte, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	delete(fields, "status")
	return yaml.Marshal(fields)
}

func objectName(obj any) string {
	if named, ok := obj.(metav1.Object); ok {
		return named.GetName()
	}
	return ""
}

// stderr is where the migration's own commentary goes, so stdout stays the
// summary a pipe can read.
var stderr = os.Stderr

func fatalf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	fmt.Fprint(os.Stderr, "pg2crd: "+message)
	os.Exit(1)
}
