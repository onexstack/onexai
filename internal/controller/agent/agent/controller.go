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

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/controller/agent/grant"
	"github.com/onexstack/onexai/internal/controller/support"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
	chatv1alpha1 "github.com/onexstack/onexai/pkg/apis/chat/v1alpha1"
)

// ControllerName is the canonical name of this controller, used by the
// controller-manager's --controllers flag to enable/disable it.
const ControllerName = "agent-controller"

// defaultMaxSkills is the cap applied when spec.maxSkills is zero.
const defaultMaxSkills = 20

// grantIndexKey carries the per-reconcile ReferenceGrant index so every
// resolver reads the same snapshot without threading a new parameter through
// each of them.
type grantIndexKey struct{}

func withGrantIndex(ctx context.Context, index *sharing.GrantIndex) context.Context {
	return context.WithValue(ctx, grantIndexKey{}, index)
}

func grantIndexFrom(ctx context.Context) *sharing.GrantIndex {
	index, _ := ctx.Value(grantIndexKey{}).(*sharing.GrantIndex)
	return index
}

// Reconciler reconciles an Agent object.
//
// It resolves the agent's references and selectors into status.resolved, which
// is what a runtime reads to know what the agent currently is. Building the
// execution graph stays in the runtime: it needs live tool schemas and a
// sandbox, neither of which belongs in a control loop.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder publishes Ready transitions as events, so `kubectl describe`
	// shows what the controller decided without opening the status.
	Recorder record.EventRecorder
}

// Add wires the reconciler into the given manager.
func Add(ctx context.Context, mgr ctrl.Manager, options controller.Options) (bool, error) {
	r := &Reconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor(ControllerName),
	}
	if err := r.SetupWithManager(ctx, mgr, options); err != nil {
		return false, err
	}
	return true, nil
}

// SetupWithManager sets up the controller with the Manager.
//
// Every resource an agent can reference is watched: adding, disabling or
// relabelling one changes what the agent resolves to, and waiting for the agent
// itself to be edited would leave status stale for as long as nobody touched it.
func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.Agent{}).
		// An agent can aggregate other agents, so editing a source changes what
		// its consumers resolve to. Only spec and label changes matter: a status
		// patch is this controller's own output, and watching it would enqueue
		// every agent in the namespace on every reconcile.
		Watches(&agentv1alpha1.Agent{},
			handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace),
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				predicate.LabelChangedPredicate{},
			)),
		).
		Watches(&agentv1alpha1.Skill{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace)).
		Watches(&agentv1alpha1.CustomCommand{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace)).
		// A binding's command selector is resolved against the catalogue's
		// labels, so a command added to the selected group changes this agent's
		// resolved command set. Generation does not change on a label rewrite.
		Watches(&chatv1alpha1.Command{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace),
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				predicate.LabelChangedPredicate{},
			))).
		Watches(&agentv1alpha1.MCPServer{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace)).
		Watches(&agentv1alpha1.KnowledgeBase{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace)).
		// A grant in the target namespace changes what the referrer namespaces
		// may resolve, so the agents named by its from list are the ones that
		// must be re-evaluated — not the agents in the grant's own namespace.
		Watches(&agentv1alpha1.ReferenceGrant{},
			handler.EnqueueRequestsFromMapFunc(r.agentsForGrant),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(&agentv1alpha1.Memory{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace)).
		Watches(&agentv1alpha1.Prompt{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace)).
		Watches(&agentv1alpha1.Model{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace)).
		Watches(&agentv1alpha1.Webhook{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace)).
		// A template is shared behavior: editing one changes what every
		// referencing agent resolves to, so the agents are re-evaluated rather
		// than waiting for each of them to be edited.
		Watches(&agentv1alpha1.AgentTemplate{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace)).
		// Every agent in the namespace is re-evaluated, not only the ones the
		// binding currently matches: a selector or label edit can remove a match,
		// and the removed agents are exactly the ones no map function can name
		// from the new object alone.
		Watches(&agentv1alpha1.AgentBinding{},
			handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace),
			builder.WithPredicates(bindingChangePredicate()),
		).
		// A cluster binding reaches across namespaces, so its own (empty)
		// namespace says nothing about which agents to re-resolve. The binding's
		// audience is the answer; the map function reads it.
		Watches(&agentv1alpha1.ClusterAgentBinding{},
			handler.EnqueueRequestsFromMapFunc(r.agentsForClusterBinding),
			builder.WithPredicates(clusterBindingChangePredicate()),
		).
		// An A2A credential rotation or a RuntimeProfile endpoint change must
		// re-sync the card and the resolved endpoint without waiting for the
		// agent itself to be edited.
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace)).
		Watches(&agentv1alpha1.RuntimeProfile{}, handler.EnqueueRequestsFromMapFunc(r.allAgentsInNamespace)).
		Named(ControllerName).
		WithOptions(options).
		Complete(r)
}

// Reconcile resolves the agent and reports the result in status.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	agent := &agentv1alpha1.Agent{}
	if err := r.Get(ctx, req.NamespacedName, agent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if agent.DeletionTimestamp != nil {
		// The garbage collector removes the derived Prompt with the agent.
		return ctrl.Result{}, nil
	}

	// An external agent has no platform-defined behavior: its whole contract is
	// the remote AgentCard. Resolving prompts/capabilities for it would create
	// derived objects nobody uses and report a Ready that ignores the card.
	if agent.Spec.Kind == agentv1alpha1.AgentKindExternal {
		return r.reconcileExternal(ctx, agent)
	}

	result, err := r.resolve(ctx, agent)
	if err != nil {
		return ctrl.Result{}, err
	}

	outcome, err := r.reconcileAggregatedPrompt(ctx, agent, result.Prompts)
	if err != nil {
		return ctrl.Result{}, err
	}
	result.Resolved.Prompt = outcome.Name
	conditions := append(result.Conditions, outcome.Condition)

	status := agent.Status.DeepCopy()
	status.ObservedGeneration = agent.Generation
	status.Resolved = result.Resolved
	status.ResolvedHash = hashResolved(result.Resolved)
	status.Missing = result.Missing
	recordUnresolved(result.Missing)
	status.BindingContributions = result.BindingContributions
	status.BindingContributionsTotal = result.BindingContributionsTotal
	status.BindingContributionsTruncated = result.BindingContributionsTruncated
	status.AggregatedPrompt = outcome.Name
	status.AggregatedInputHash = outcome.InputHash
	status.AggregatedPromptHash = outcome.ContentHash
	status.AggregatedPromptOverridden = outcome.Overridden
	status.AggregatedPromptStale = outcome.Stale
	status.ObservedRegenerateToken = outcome.ObservedToken
	status.Conditions = nil
	for _, condition := range conditions {
		if condition.Type == "" {
			continue
		}
		apimeta.SetStatusCondition(&status.Conditions, condition)
	}
	support.ReportCondition(r.Recorder, agent, &status.Conditions, readyCondition(conditions, agent.Generation))

	// A native agent can execute on an A2A runtime. Its behavior is still the
	// platform's, but the execution contract is the remote card, so the two
	// states are reported together.
	nextRequeue := outcome.RequeueAfter
	if agent.Spec.Execution != nil && agent.Spec.Execution.Mode == agentv1alpha1.ExecutionModeA2A {
		a2aResult := r.syncA2A(ctx, agent)
		status.A2A = a2aResult.Status
		for _, condition := range a2aResult.Conditions {
			if condition.Type != "" {
				apimeta.SetStatusCondition(&status.Conditions, condition)
			}
		}
		if !a2aResult.Ready {
			support.SetReady(r.Recorder, agent, &status.Conditions, agent.Generation, false,
				"SyncFailed", a2aResult.Message)
		}
		if a2aResult.RequeueAfter > 0 && (nextRequeue == 0 || a2aResult.RequeueAfter < nextRequeue) {
			nextRequeue = a2aResult.RequeueAfter
		}
	} else {
		status.A2A = nil
	}

	if apiequality.Semantic.DeepEqual(&agent.Status, status) {
		return requeueAfter(nextRequeue), nil
	}

	before := agent.DeepCopy()
	agent.Status = *status
	if err := r.Status().Patch(ctx, agent, client.MergeFrom(before)); err != nil {
		// A delete that lands between the read and the status write is not an
		// error; the next event (if any) is a delete, not a reconcile.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("resolved agent", "agent", agent.Name,
		"prompts", len(result.Resolved.Prompts), "skills", len(result.Resolved.Skills),
		"mcps", len(result.Resolved.MCPs), "knowledgeBases", len(result.Resolved.KnowledgeBases))
	return requeueAfter(nextRequeue), nil
}

func requeueAfter(after time.Duration) ctrl.Result {
	if after > 0 {
		return ctrl.Result{RequeueAfter: after}
	}
	return ctrl.Result{}
}

// resolveResult is the resolved view plus the prompt set the aggregation step
// needs.
type resolveResult struct {
	Resolved                      agentv1alpha1.AgentResolved
	Conditions                    []metav1.Condition
	Missing                       []string
	Prompts                       promptSet
	BindingContributions          []agentv1alpha1.AgentBindingContribution
	BindingContributionsTotal     int32
	BindingContributionsTruncated bool
}

// resolve produces the resolved view and the conditions that explain it.
func (r *Reconciler) resolve(ctx context.Context, agent *agentv1alpha1.Agent) (resolveResult, error) {
	var result resolveResult

	grants, err := grant.Index(ctx, r.Client)
	if err != nil {
		return result, err
	}
	ctx = withGrantIndex(ctx, grants)

	bindings, err := r.resolveBindings(ctx, agent)
	if err != nil {
		return result, err
	}
	missing := append([]string(nil), bindings.missing...)

	templates, err := r.resolveTemplates(ctx, agent)
	if err != nil {
		return result, err
	}
	missing = append(missing, templates.missing...)

	prompts, err := r.resolvePromptSetWith(ctx, agent,
		append(promptReferences(bindings.prompts), templates.prompts...),
		templates.promptSelectors...)
	if err != nil {
		return result, err
	}
	missing = append(missing, prompts.missing...)

	if agent.Spec.ModelRef != nil {
		// resolveAggregationModel honours an explicit namespace, so existence
		// has to be checked in the same place. Two answers about one field
		// would report a model missing while it is in use.
		modelNamespace := agent.Namespace
		if agent.Spec.ModelRef.Namespace != "" {
			modelNamespace = agent.Spec.ModelRef.Namespace
		}
		reported := agent.Spec.ModelRef.Name
		if modelNamespace != agent.Namespace {
			reported = modelNamespace + "/" + reported
		}
		_, ok, err := r.exists(ctx, modelNamespace, agent.Spec.ModelRef.Name, &agentv1alpha1.Model{})
		if err != nil {
			return result, err
		}
		if ok && modelNamespace == agent.Namespace {
			result.Resolved.Model = agent.Spec.ModelRef.Name
			result.Resolved.ModelNamespace = modelNamespace
		} else if ok && len(grantWildcard(ctx, agent.Namespace, modelNamespace,
			sharing.KindModel, agent.Spec.ModelRef.Name)) > 0 {
			result.Resolved.Model = agent.Spec.ModelRef.Name
			result.Resolved.ModelNamespace = modelNamespace
		} else if ok {
			missing = append(missing, "Model/"+reported+" (namespace not allowed)")
		} else {
			missing = append(missing, "Model/"+reported)
		}
	} else if templates.model != "" {
		// The template's model is the fallback only when the agent declares
		// none; an explicit agent field always wins. A template is same-namespace
		// by construction, so its model needs no grant.
		result.Resolved.Model = templates.model
		result.Resolved.ModelNamespace = agent.Namespace
	} else if bindings.modelRef != nil {
		// A binding supplies a model only when neither the agent nor its
		// template names one. The binding controller has already reported a
		// conflict when two bindings disagree.
		modelNamespace := agent.Namespace
		if bindings.modelRef.Namespace != "" {
			modelNamespace = bindings.modelRef.Namespace
		}
		reported := bindings.modelRef.Name
		if modelNamespace != agent.Namespace {
			reported = modelNamespace + "/" + reported
		}
		_, ok, err := r.exists(ctx, modelNamespace, bindings.modelRef.Name, &agentv1alpha1.Model{})
		if err != nil {
			return result, err
		}
		if ok && modelNamespace == agent.Namespace {
			result.Resolved.Model = bindings.modelRef.Name
			result.Resolved.ModelNamespace = modelNamespace
		} else if ok && len(grantWildcard(ctx, agent.Namespace, modelNamespace,
			sharing.KindModel, bindings.modelRef.Name)) > 0 {
			result.Resolved.Model = bindings.modelRef.Name
			result.Resolved.ModelNamespace = modelNamespace
		} else if ok {
			missing = append(missing, "Model/"+reported+" (namespace not allowed)")
		} else {
			missing = append(missing, "Model/"+reported)
		}
	}

	skills, skillMissing, err := r.resolveSkills(ctx, agent)
	if err != nil {
		return result, err
	}
	skills = unionNames(skills, templates.skills)
	missing = append(missing, skillMissing...)

	customCommands, commandMissing, err := r.resolveCustomCommands(ctx, agent)
	if err != nil {
		return result, err
	}
	customCommands = unionNames(customCommands, templates.customCommands)
	missing = append(missing, commandMissing...)

	mcps, mcpMissing, err := r.resolveMCPs(ctx, agent)
	if err != nil {
		return result, err
	}
	mcps = unionNames(mcps, templates.mcps)
	missing = append(missing, mcpMissing...)

	kbs, kbMissing, err := r.resolveKnowledgeBases(ctx, agent)
	if err != nil {
		return result, err
	}
	kbs = unionNames(kbs, templates.knowledgeBases)
	missing = append(missing, kbMissing...)

	webhooks := make([]string, 0, len(agent.Spec.Webhooks))
	for _, name := range agent.Spec.Webhooks {
		_, ok, err := r.exists(ctx, agent.Namespace, name, &agentv1alpha1.Webhook{})
		if err != nil {
			return result, err
		}
		if ok {
			webhooks = append(webhooks, name)
		} else {
			missing = append(missing, "Webhook/"+name)
		}
	}

	approval := unionNames(append([]string(nil), agent.Spec.RequireApproval...), templates.approval)
	webhooks = unionNames(webhooks, templates.webhooks)

	// The agents this one aggregates. A structural failure drops the whole
	// inheritance rather than a prefix of it: webhooks and approval patterns
	// travel with the capability set, so half a union is not a safe subset.
	sources, err := r.resolveAgentSources(ctx, agent)
	if err != nil {
		return result, err
	}
	missing = append(missing, sources.missing...)

	var inherited inheritedSets
	if sources.usable() && len(sources.sources) > 0 {
		var (
			mergedPrompts    promptSet
			inheritedMissing []string
		)
		inherited, mergedPrompts, inheritedMissing, err = r.collectInherited(ctx, sources, prompts)
		if err != nil {
			return result, err
		}
		missing = append(missing, inheritedMissing...)
		if mergedPrompts.tooMany {
			// The prompt union would exceed its cap. Dropping the whole
			// inheritance is the only merge that cannot silently change the
			// derived prompt.
			sources.tooManyPrompts = true
		} else {
			prompts = mergedPrompts
			skills = unionNames(skills, inherited.skills)
			customCommands = unionNames(customCommands, inherited.customCommands)
			mcps = unionNames(mcps, inherited.mcps)
			kbs = unionNames(kbs, inherited.knowledgeBases)
			webhooks = unionNames(webhooks, inherited.webhooks)
			approval = unionNames(approval, inherited.requireApproval)
			result.Resolved.Agents = inherited.agents
		}
	}
	if !sources.usable() || len(sources.missing) > 0 {
		// The derived prompt keeps its last good value: a source that is
		// momentarily unavailable must not read as "this agent has no prompt".
		prompts.sourceUnavailable = true
	}

	// The binding layer is purely additive, exactly like the agent's own
	// selectors. It is merged after inheritance so a source agent's binding
	// travels with the source.
	result.Prompts = prompts
	result.Resolved.Prompts = prompts.availableNames()
	result.Resolved.Skills = unionNames(skills, bindings.skills)
	result.Resolved.CustomCommands = unionNames(customCommands, bindings.customCommands)
	result.Resolved.MCPs = unionNames(mcps, bindings.mcps)
	result.Resolved.KnowledgeBases = unionNames(kbs, bindings.knowledgeBases)
	result.Resolved.Commands = unionNames(bindings.commands, inherited.commands)
	result.Resolved.Webhooks = webhooks
	result.Resolved.RequireApproval = approval
	result.Resolved.Templates = templates.names
	calls, callMissing, callErr := r.resolveCalls(ctx, agent)
	if callErr != nil {
		return result, callErr
	}
	missing = append(missing, callMissing...)
	result.Resolved.Calls = calls
	result.Resolved.Delegates = unionNames(referenceNames(agent.Spec.Delegates), templates.delegates)
	result.Resolved.SessionTools = agent.Spec.SessionTools || templates.sessionTools
	if agent.Spec.OutputSchema != nil {
		result.Resolved.OutputSchema = agent.Spec.OutputSchema
		result.Resolved.OutputSchemaStrict = agent.Spec.OutputSchemaStrict
	} else if templates.outputSchema != nil {
		result.Resolved.OutputSchema = templates.outputSchema
		result.Resolved.OutputSchemaStrict = templates.outputSchemaStrict
	}
	memoryRef := agent.Spec.MemoryRef
	if memoryRef == nil {
		memoryRef = templates.memoryRef
	}
	// The binding layer is a fallback, not an override: an agent that declares
	// its own memory keeps it.
	if memoryRef == nil {
		memoryRef = bindings.memoryRef
	}
	if memoryRef != nil {
		memoryNamespace := memoryRef.Namespace
		if memoryNamespace == "" {
			memoryNamespace = agent.Namespace
		}
		qualified := model.CanonicalRef(memoryNamespace, memoryRef.Name)
		memory := &agentv1alpha1.Memory{}
		err := r.Get(ctx, types.NamespacedName{Namespace: memoryNamespace, Name: memoryRef.Name}, memory)
		switch {
		case client.IgnoreNotFound(err) == nil && err != nil:
			missing = append(missing, "Memory/"+qualified)
		case err != nil:
			return result, err
		case !memory.Spec.Enabled:
			missing = append(missing, "Memory/"+qualified+" (disabled)")
		case memory.Namespace != agent.Namespace &&
			len(grantWildcard(ctx, agent.Namespace, memory.Namespace, sharing.KindMemory, memory.Name)) == 0:
			missing = append(missing, "Memory/"+qualified+" (namespace not allowed)")
		default:
			autoSave := int32(0)
			if memory.Spec.Extraction.Enabled {
				autoSave = 1
			}
			embeddingModel := ""
			if memory.Spec.Embedding.ModelRef != nil {
				embeddingModel = memory.Spec.Embedding.ModelRef.Name
			}
			result.Resolved.Memory = &agentv1alpha1.AgentMemorySpec{
				Namespace:      memory.Namespace,
				Name:           memory.Name,
				Enabled:        true,
				EmbeddingModel: embeddingModel,
				TTLDays:        memory.Spec.Retention.TTLDays,
				AutoSaveEvery:  autoSave,
			}
		}
	}
	result.BindingContributions = bindings.contributions
	result.BindingContributionsTotal = bindings.total
	result.BindingContributionsTruncated = bindings.truncated

	result.Conditions = append(result.Conditions, aggregatedCondition(agent, sources))
	result.Conditions = append(result.Conditions, templatesCondition(agent, templates))
	result.Conditions = append(result.Conditions, bindingsCondition(agent, bindings))

	result.Conditions = append(result.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionRefsResolved,
		Status:             boolCondition(len(missing) == 0),
		Reason:             reasonFor(len(missing) == 0, "AllRefsFound", "ReferenceNotFound"),
		Message:            missingMessage(missing),
		ObservedGeneration: agent.Generation,
	})

	maxSkills := int(agent.Spec.MaxSkills)
	if maxSkills == 0 {
		maxSkills = defaultMaxSkills
	}
	withinBudget := len(result.Resolved.Skills) <= maxSkills
	result.Conditions = append(result.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionSkillsWithinBudget,
		Status:             boolCondition(withinBudget),
		Reason:             reasonFor(withinBudget, "WithinBudget", "OverBudget"),
		Message:            fmt.Sprintf("resolved %d skill(s), cap %d", len(result.Resolved.Skills), maxSkills),
		ObservedGeneration: agent.Generation,
	})

	result.Missing = missing
	return result, nil
}

func (r *Reconciler) resolveSkills(ctx context.Context, agent *agentv1alpha1.Agent) ([]string, []string, error) {
	return r.resolveSkillsIn(ctx, agent.Namespace, agent.Spec.Skills, agent.Spec.SkillSelector)
}

func (r *Reconciler) resolveSkillsIn(
	ctx context.Context, namespace string, refs []agentv1alpha1.Reference, selector *metav1.LabelSelector,
) ([]string, []string, error) {
	return resolveSet(ctx, namespace, refs, selector,
		func(ctx context.Context, ns, name string) ([]string, bool, error) {
			allowed, ok, err := r.exists(ctx, ns, name, &agentv1alpha1.Skill{})
			if err != nil || !ok {
				return allowed, ok, err
			}
			return append(allowed, grantWildcard(ctx, namespace, ns, sharing.KindSkill, name)...), true, nil
		},
		func(ctx context.Context, ns string, selector labels.Selector) ([]string, error) {
			list := &agentv1alpha1.SkillList{}
			return r.listEnabled(ctx, ns, selector, list,
				func() int { return len(list.Items) },
				func(i int) client.Object { return &list.Items[i] })
		}, "Skill")
}

func (r *Reconciler) resolveCustomCommands(
	ctx context.Context, agent *agentv1alpha1.Agent,
) ([]string, []string, error) {
	return r.resolveCustomCommandsIn(ctx, agent.Namespace, agent.Spec.CustomCommands, agent.Spec.CustomCommandSelector)
}

func (r *Reconciler) resolveCustomCommandsIn(
	ctx context.Context, namespace string, refs []agentv1alpha1.Reference, selector *metav1.LabelSelector,
) ([]string, []string, error) {
	return resolveSet(ctx, namespace, refs, selector,
		func(ctx context.Context, ns, name string) ([]string, bool, error) {
			allowed, ok, err := r.exists(ctx, ns, name, &agentv1alpha1.CustomCommand{})
			if err != nil || !ok {
				return allowed, ok, err
			}
			return append(allowed, grantWildcard(ctx, namespace, ns, sharing.KindCustomCommand, name)...), true, nil
		},
		func(ctx context.Context, ns string, selector labels.Selector) ([]string, error) {
			list := &agentv1alpha1.CustomCommandList{}
			return r.listEnabled(ctx, ns, selector, list,
				func() int { return len(list.Items) },
				func(i int) client.Object { return &list.Items[i] })
		}, "CustomCommand")
}

func (r *Reconciler) resolveMCPs(ctx context.Context, agent *agentv1alpha1.Agent) ([]string, []string, error) {
	return r.resolveMCPsIn(ctx, agent.Namespace, agent.Spec.MCPs, agent.Spec.MCPServerSelector)
}

func (r *Reconciler) resolveMCPsIn(
	ctx context.Context, namespace string, refs []agentv1alpha1.Reference, selector *metav1.LabelSelector,
) ([]string, []string, error) {
	return resolveSet(ctx, namespace, refs, selector,
		func(ctx context.Context, ns, name string) ([]string, bool, error) {
			allowed, ok, err := r.exists(ctx, ns, name, &agentv1alpha1.MCPServer{})
			if err != nil || !ok {
				return allowed, ok, err
			}
			return append(allowed, grantWildcard(ctx, namespace, ns, sharing.KindMCPServer, name)...), true, nil
		},
		func(ctx context.Context, ns string, selector labels.Selector) ([]string, error) {
			list := &agentv1alpha1.MCPServerList{}
			return r.listEnabled(ctx, ns, selector, list,
				func() int { return len(list.Items) },
				func(i int) client.Object { return &list.Items[i] })
		}, "MCPServer")
}

func (r *Reconciler) resolveKnowledgeBases(ctx context.Context, agent *agentv1alpha1.Agent) ([]string, []string, error) {
	return r.resolveKnowledgeBasesIn(ctx, agent.Namespace, agent.Spec.KnowledgeBases, agent.Spec.KnowledgeBaseSelector)
}

func (r *Reconciler) resolveKnowledgeBasesIn(
	ctx context.Context, namespace string, refs []agentv1alpha1.Reference, selector *metav1.LabelSelector,
) ([]string, []string, error) {
	return resolveSet(ctx, namespace, refs, selector,
		func(ctx context.Context, ns, name string) ([]string, bool, error) {
			allowed, ok, err := r.exists(ctx, ns, name, &agentv1alpha1.KnowledgeBase{})
			if err != nil || !ok {
				return allowed, ok, err
			}
			return append(allowed, grantWildcard(ctx, namespace, ns, sharing.KindKnowledgeBase, name)...), true, nil
		},
		func(ctx context.Context, ns string, selector labels.Selector) ([]string, error) {
			list := &agentv1alpha1.KnowledgeBaseList{}
			return r.listEnabled(ctx, ns, selector, list,
				func() int { return len(list.Items) },
				func(i int) client.Object { return &list.Items[i] })
		}, "KnowledgeBase")
}

// listEnabled returns the names of the enabled objects a selector matches.
//
// It goes through enabled() like the named-reference path does, so "a disabled
// object is invisible" is one rule rather than one per kind — and a kind whose
// case is missing from enabled() fails closed on both paths at once.
func (r *Reconciler) listEnabled(
	ctx context.Context, namespace string, selector labels.Selector,
	list client.ObjectList, count func() int, at func(int) client.Object,
) ([]string, error) {
	if err := r.List(ctx, list, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	names := make([]string, 0, count())
	for i := 0; i < count(); i++ {
		item := at(i)
		if !enabled(item) {
			continue
		}
		names = append(names, item.GetName())
	}
	return names, nil
}

// resolveSet applies "names ∪ selector" and reports which names are missing.
//
// The selector only ever matches in the agent's own namespace: selectors do not
// cross namespaces in Kubernetes, while a named reference may.
func resolveSet(
	ctx context.Context, namespace string,
	refs []agentv1alpha1.Reference, selector *metav1.LabelSelector,
	exists func(context.Context, string, string) ([]string, bool, error),
	listBySelector func(context.Context, string, labels.Selector) ([]string, error),
	kind string,
) ([]string, []string, error) {
	seen := make(map[string]struct{}, len(refs))
	names := make([]string, 0, len(refs))
	var missing []string

	for _, ref := range refs {
		ns := ref.Namespace
		if ns == "" {
			ns = namespace
		}
		allowed, ok, err := exists(ctx, ns, ref.Name)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			missing = append(missing, kind+"/"+ns+"/"+ref.Name)
			continue
		}
		// A cross-namespace target must be admitted by a ReferenceGrant in its
		// namespace. Reporting it here keeps status.resolved from advertising a
		// reference the run will refuse.
		if ns != namespace && !slices.Contains(allowed, sharing.AllowAll) {
			missing = append(missing, kind+"/"+ns+"/"+ref.Name+" (namespace not allowed)")
			continue
		}
		// Cross-namespace references are reported qualified, so a resolver can
		// tell two same-named skills in different namespaces apart.
		name := ref.Name
		if ns != namespace {
			name = ns + "/" + ref.Name
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}

	if selector != nil {
		matched, err := listBySelector(ctx, namespace, mustSelector(selector))
		if err != nil {
			return nil, nil, err
		}
		for _, name := range matched {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}

	sort.Strings(names)
	return names, missing, nil
}

// exists reports whether the object is present and enabled.
// exists reports the object's sharing declaration along with whether it is
// present and enabled. The declaration travels with the answer so the caller can
// decide whether a cross-namespace reference is admissible without a second read.
func (r *Reconciler) exists(
	ctx context.Context, namespace, name string, obj client.Object,
) ([]string, bool, error) {
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil, false, nil
		}
		return nil, false, err
	}

	// Enabled is the soft-delete switch on every resource in this group. A
	// disabled object is invisible to resolution, so naming one is as good as
	// naming nothing.
	return nil, enabled(obj), nil
}

// enabled reads the soft-delete switch, which every resource in this group
// carries in its embedded CommonSpec.
//
// A kind this function does not know is reported as disabled on purpose: the
// caller would otherwise treat it as reachable, and a newly added, gated
// resource must not become visible through a missing case.
func enabled(obj client.Object) bool {
	switch o := obj.(type) {
	case *agentv1alpha1.Agent:
		return o.Spec.Enabled
	case *agentv1alpha1.AgentTemplate:
		return o.Spec.Enabled
	case *agentv1alpha1.Skill:
		return o.Spec.Enabled
	case *agentv1alpha1.MCPServer:
		return o.Spec.Enabled
	case *agentv1alpha1.KnowledgeBase:
		return o.Spec.Enabled
	case *agentv1alpha1.Prompt:
		return o.Spec.Enabled
	case *agentv1alpha1.Model:
		return o.Spec.Enabled
	case *agentv1alpha1.Webhook:
		return o.Spec.Enabled
	case *agentv1alpha1.CustomCommand:
		return o.Spec.Enabled
	default:
		return false
	}
}

// allAgentsInNamespace enqueues every agent in the changed object's namespace.
func (r *Reconciler) allAgentsInNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	agents := &agentv1alpha1.AgentList{}
	if err := r.List(ctx, agents, client.InNamespace(obj.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to list agents for a referenced resource", "object", obj.GetName())
		return nil
	}

	requests := make([]reconcile.Request, 0, len(agents.Items))
	for i := range agents.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: agents.Items[i].Namespace, Name: agents.Items[i].Name},
		})
	}
	return requests
}

// agentsForClusterBinding enqueues the agents a cluster binding can reach.
//
// The binding is cluster-scoped, so there is no namespace on the object to map
// from: its audience is the answer. A wildcard names every namespace on
// purpose, so every agent is enqueued — including ones that existed before the
// binding, which is what makes a cluster-wide grant take effect immediately
// instead of on the next unrelated event.
func (r *Reconciler) agentsForClusterBinding(ctx context.Context, obj client.Object) []reconcile.Request {
	binding, ok := obj.(*agentv1alpha1.ClusterAgentBinding)
	if !ok {
		return nil
	}

	seen := make(map[types.NamespacedName]struct{})
	var requests []reconcile.Request

	enqueue := func(listOptions ...client.ListOption) {
		agents := &agentv1alpha1.AgentList{}
		if err := r.List(ctx, agents, listOptions...); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "failed to list agents for a cluster agent binding",
				"binding", binding.Name)
			return
		}
		for i := range agents.Items {
			key := types.NamespacedName{Namespace: agents.Items[i].Namespace, Name: agents.Items[i].Name}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			requests = append(requests, reconcile.Request{NamespacedName: key})
		}
	}

	if agentvalidation.IsAllNamespaces(binding.Spec.Namespaces) {
		enqueue()
		return requests
	}
	for _, namespace := range binding.Spec.Namespaces {
		enqueue(client.InNamespace(namespace))
	}
	return requests
}

// grantWildcard folds a matching ReferenceGrant into the legacy allow-list the
// resolver already understands. It returns the wildcard sentinel only for a
// cross-namespace reference the grant permits; same-namespace and refused
// references return nil so the existing sharing.Allows check reaches its own
// answer.
func grantWildcard(ctx context.Context, referrerNamespace, targetNamespace, targetKind, targetName string) []string {
	if referrerNamespace == targetNamespace {
		return nil
	}
	// A kind the sharing table marks unshareable must not become reachable
	// through a grant: custom commands run with the caller's authority, and the
	// same-namespace kinds are addressed in one namespace by construction.
	if !sharing.Shareable(targetKind) {
		return nil
	}
	if grant.Allows(grantIndexFrom(ctx), referrerNamespace, targetNamespace, targetKind, targetName) {
		return []string{sharing.AllowAll}
	}
	return nil
}

// agentsForGrant enqueues the agents a grant can affect. A grant lives in the
// target namespace but changes what its from namespaces may resolve, so the
// targets of the event are the referrers — not the agents next to the grant.
func (r *Reconciler) agentsForGrant(ctx context.Context, obj client.Object) []reconcile.Request {
	refGrant, ok := obj.(*agentv1alpha1.ReferenceGrant)
	if !ok {
		return nil
	}
	seen := make(map[types.NamespacedName]struct{})
	var requests []reconcile.Request
	for _, from := range refGrant.Spec.From {
		if from.Group != sharing.GroupName || from.Kind != grant.AgentKind {
			continue
		}
		var agents agentv1alpha1.AgentList
		if err := r.List(ctx, &agents, client.InNamespace(from.Namespace)); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "failed to list agents for a reference grant",
				"grant", refGrant.Name, "namespace", refGrant.Namespace)
			continue
		}
		for i := range agents.Items {
			key := types.NamespacedName{Namespace: agents.Items[i].Namespace, Name: agents.Items[i].Name}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			requests = append(requests, reconcile.Request{NamespacedName: key})
		}
	}
	return requests
}

func hashResolved(resolved agentv1alpha1.AgentResolved) string {
	raw, err := json.Marshal(resolved)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readyCondition(conditions []metav1.Condition, generation int64) metav1.Condition {
	ready := true
	var message string
	for _, condition := range conditions {
		if condition.Type == "" {
			continue
		}
		if condition.Type == agentv1alpha1.ConditionPromptAggregated {
			// Aggregation is derived data with a last-good value; a failure
			// there is reported on its own condition and does not make the
			// agent unusable.
			continue
		}
		if condition.Status != metav1.ConditionTrue {
			ready = false
			if message == "" {
				message = condition.Message
			}
		}
	}
	if message == "" {
		message = "every reference resolved and the capability set is within budget"
	}
	return metav1.Condition{
		Type:               agentv1alpha1.ConditionReady,
		Status:             boolCondition(ready),
		Reason:             reasonFor(ready, "Resolved", "ResolutionFailed"),
		Message:            message,
		ObservedGeneration: generation,
	}
}

func boolCondition(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func reasonFor(ok bool, whenTrue, whenFalse string) string {
	if ok {
		return whenTrue
	}
	return whenFalse
}

func missingMessage(missing []string) string {
	if len(missing) == 0 {
		return "every referenced object exists and is enabled"
	}
	return fmt.Sprintf("missing or disabled: %v", missing)
}

func mustSelector(selector *metav1.LabelSelector) labels.Selector {
	parsed, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		// The API server validated the selector on write.
		return labels.Nothing()
	}
	return parsed
}

// resolveCalls resolves the agents this agent may call as tools.
//
// A native target is usable as soon as it is enabled; an external target also
// needs a synced card, because without one there is no endpoint to call. A
// missing target is recorded rather than fatal: the agent still runs with the
// tools it has, and RefsResolved explains the omission.
func (r *Reconciler) resolveCalls(
	ctx context.Context, agent *agentv1alpha1.Agent,
) ([]string, []string, error) {
	var calls, missing []string
	for _, ref := range agent.Spec.Calls {
		target := &agentv1alpha1.Agent{}
		err := r.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: ref.Name}, target)
		switch {
		case client.IgnoreNotFound(err) == nil && err != nil:
			missing = append(missing, "Agent/"+ref.Name)
			continue
		case err != nil:
			return nil, nil, err
		case !target.Spec.Enabled:
			missing = append(missing, "Agent/"+ref.Name+" (disabled)")
			continue
		}
		if target.Spec.Kind == agentv1alpha1.AgentKindExternal {
			if target.Status.A2A == nil || target.Status.A2A.CardDigest == "" {
				missing = append(missing, "Agent/"+ref.Name+" (no card)")
				continue
			}
			if target.Spec.Sharing != nil && len(target.Spec.Sharing.AllowedNamespaces) > 0 &&
				!slices.Contains(target.Spec.Sharing.AllowedNamespaces, agent.Namespace) {
				missing = append(missing, "Agent/"+ref.Name+" (namespace not allowed)")
				continue
			}
		}
		calls = append(calls, ref.Name)
	}
	return calls, missing, nil
}
