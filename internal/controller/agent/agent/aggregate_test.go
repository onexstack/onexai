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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func testAgent(name string) *agentv1alpha1.Agent {
	return &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
}

func testPromptSet(templates ...string) promptSet {
	set := promptSet{}
	for i, template := range templates {
		name := "p" + string(rune('1'+i))
		set.declared = append(set.declared, promptRef{Namespace: "default", Name: name})
		set.available = append(set.available, promptSource{
			Name:      name,
			Namespace: "default",
			Template:  template,
		})
	}
	return set
}

func TestBuildAggregateSingleSource(t *testing.T) {
	agent := testAgent("a")
	set := testPromptSet("You are helpful.")
	built := (&Reconciler{}).buildAggregate(context.Background(), agent, set, aggregationModel{}, metav1.Condition{})
	if built.Content != "You are helpful." {
		t.Fatalf("content = %q", built.Content)
	}
	if built.Condition.Reason != "SinglePromptCopied" {
		t.Fatalf("reason = %q", built.Condition.Reason)
	}
}

func TestBuildAggregateMultiSource(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"MERGED"}}]}`)
	}))
	defer server.Close()

	agent := testAgent("a")
	set := testPromptSet("First persona.", "Second persona.")
	model := aggregationModel{
		Name:           "aggregator",
		Endpoint:       server.URL,
		Model:          "test-model",
		Timeout:        5 * time.Second,
		MaxTokens:      4000,
		MaxInputTokens: 60000,
	}
	built := (&Reconciler{}).buildAggregate(context.Background(), agent, set, model, metav1.Condition{})
	if built.Content != "MERGED" {
		t.Fatalf("content = %q", built.Content)
	}
	if built.Condition.Reason != "Aggregated" {
		t.Fatalf("reason = %q", built.Condition.Reason)
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatalf("the aggregation endpoint was not called")
	}
}

func TestBuildAggregateFallsBackWithoutModel(t *testing.T) {
	agent := testAgent("a")
	set := testPromptSet("First persona.", "Second persona.")
	built := (&Reconciler{}).buildAggregate(context.Background(), agent, set, aggregationModel{},
		promptCondition(agent, false, "ModelMissing", "no model"))
	if !built.Fallback {
		t.Fatalf("expected a fallback")
	}
	if !strings.Contains(built.Content, "First persona.") || !strings.Contains(built.Content, "Second persona.") {
		t.Fatalf("fallback lost a source: %q", built.Content)
	}
	if built.Condition.Reason != "ModelMissing" {
		t.Fatalf("reason = %q", built.Condition.Reason)
	}
}

func TestAggregatedOverridden(t *testing.T) {
	agent := testAgent("a")
	prompt := &agentv1alpha1.Prompt{
		ObjectMeta: metav1.ObjectMeta{
			Name: "aggregated-a",
			Annotations: map[string]string{
				agentv1alpha1.AnnotationAggregatedContentHash: hashPromptContent(true, "generated", nil),
			},
		},
		Spec: agentv1alpha1.PromptSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
			Template:   "generated",
		},
	}
	if aggregatedOverridden(prompt, agent) {
		t.Fatalf("an unedited object was treated as overridden")
	}
	prompt.Spec.Template = "manual"
	if !aggregatedOverridden(prompt, agent) {
		t.Fatalf("a manual edit was not detected")
	}
}

func TestHashPromptInputsChangesWithSource(t *testing.T) {
	first := testPromptSet("First.", "Second.")
	second := testPromptSet("First.", "Second.")
	if hashPromptInputs(first, "model", 4000) != hashPromptInputs(second, "model", 4000) {
		t.Fatalf("the same inputs produced different hashes")
	}
	second.available[1].Template = "Changed."
	if hashPromptInputs(first, "model", 4000) == hashPromptInputs(second, "model", 4000) {
		t.Fatalf("a changed source did not change the input hash")
	}
}

// recordingClient is memoryClient plus the three writes
// reconcileAggregatedPrompt makes. Writes land in the object list the reads
// come from, so a second reconcile sees what the API server would have stored —
// which is what makes the idempotency assertions below meaningful.
type recordingClient struct {
	*memoryClient
	creates int
	updates int
	deletes []string
}

func (c *recordingClient) Create(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
	prompt, ok := obj.(*agentv1alpha1.Prompt)
	if !ok {
		return fmt.Errorf("unexpected create of %T", obj)
	}
	c.creates++
	c.memoryClient.objects = append(c.memoryClient.objects, prompt.DeepCopy())
	return nil
}

func (c *recordingClient) Update(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
	prompt, ok := obj.(*agentv1alpha1.Prompt)
	if !ok {
		return fmt.Errorf("unexpected update of %T", obj)
	}
	c.updates++
	for i, stored := range c.memoryClient.objects {
		if stored.GetNamespace() == prompt.Namespace && stored.GetName() == prompt.Name {
			c.memoryClient.objects[i] = prompt.DeepCopy()
			return nil
		}
	}
	return fmt.Errorf("update of %s/%s that was never created", prompt.Namespace, prompt.Name)
}

func (c *recordingClient) Delete(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
	c.deletes = append(c.deletes, obj.GetName())
	for i, stored := range c.memoryClient.objects {
		if stored.GetNamespace() == obj.GetNamespace() && stored.GetName() == obj.GetName() {
			c.memoryClient.objects = append(c.memoryClient.objects[:i], c.memoryClient.objects[i+1:]...)
			break
		}
	}
	return nil
}

func (c *recordingClient) writes() int { return c.creates + c.updates + len(c.deletes) }

// prompt returns the stored Prompt by name, or nil.
func (c *recordingClient) prompt(name string) *agentv1alpha1.Prompt {
	for _, object := range c.memoryClient.objects {
		if p, ok := object.(*agentv1alpha1.Prompt); ok && p.Name == name {
			return p
		}
	}
	return nil
}

func newAggregateRecorder(t *testing.T, objs ...client.Object) (*Reconciler, *recordingClient) {
	t.Helper()

	rec := newFakeReconciler(t, objs...)
	mem, ok := rec.Client.(*memoryClient)
	if !ok {
		t.Fatalf("unexpected client %T", rec.Client)
	}
	recorder := &recordingClient{memoryClient: mem}
	rec.Client = recorder
	return rec, recorder
}

// aggregateOf builds the derived Prompt the way the controller writes it: an
// owner reference, plus both hashes matching the content.
func aggregateOf(agent *agentv1alpha1.Agent, content, inputHash string) *agentv1alpha1.Prompt {
	annotations := map[string]string{
		agentv1alpha1.AnnotationAggregatedFor:         agent.Name,
		agentv1alpha1.AnnotationAggregatedContentHash: hashPromptContent(true, content, nil),
	}
	if inputHash != "" {
		annotations[agentv1alpha1.AnnotationAggregatedInputHash] = inputHash
	}
	return &agentv1alpha1.Prompt{
		ObjectMeta: metav1.ObjectMeta{
			Name:            agentv1alpha1.AggregatedPromptName(agent.Name),
			Namespace:       agent.Namespace,
			OwnerReferences: []metav1.OwnerReference{agentv1alpha1.AgentOwnerReference(agent.Name, agent.UID)},
			Annotations:     annotations,
		},
		Spec: agentv1alpha1.PromptSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
			Template:   content,
		},
	}
}

// aggregateWant is the part of an outcome the decision table pins down.
type aggregateWant struct {
	name       string
	reason     string
	status     metav1.ConditionStatus
	overridden bool
	stale      bool
	requeue    time.Duration
	writes     int
}

// The function decides among create, update, keep verbatim, keep and mark
// stale, and delete. The risk in touching it is that one branch quietly starts
// answering for another — a change that compiles and reads plausibly. Each row
// is one state, and the assertions are the answer that state must keep getting.
func TestReconcileAggregatedPromptDecisions(t *testing.T) {
	const aggregate = "aggregated-a"

	agent := testAgent("a")
	owned := aggregateOf(agent, "generated", "sha256:old")

	// The annotations say "generated", the spec says something else: an object
	// edited outside the controller.
	edited := aggregateOf(agent, "generated", "sha256:old")
	edited.Spec.Template = "edited by hand"

	foreign := aggregateOf(&agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"}}, "x", "")
	foreign.Name = aggregate
	foreign.Namespace = "default"

	dying := testAgent("a")
	now := metav1.Now()
	dying.DeletionTimestamp = &now
	dying.Finalizers = []string{"test"}

	empty := testPromptSet("   ")
	partial := testPromptSet("First.", "Second.")
	partial.available = partial.available[:1]

	tests := []struct {
		name    string
		agent   *agentv1alpha1.Agent
		set     promptSet
		objects []client.Object
		want    aggregateWant
		check   func(t *testing.T, c *recordingClient)
	}{
		{
			name:  "creates the aggregate for a single source",
			agent: agent,
			set:   testPromptSet("You are helpful."),
			want: aggregateWant{
				name:   aggregate,
				reason: "SinglePromptCopied",
				status: metav1.ConditionTrue,
				writes: 1,
			},
			check: func(t *testing.T, c *recordingClient) {
				t.Helper()
				if got := c.prompt(aggregate); got == nil || got.Spec.Template != "You are helpful." {
					t.Fatalf("stored prompt = %+v", got)
				}
			},
		},
		{
			name:  "does nothing when no prompt is bound and nothing exists",
			agent: agent,
			set:   promptSet{},
			want:  aggregateWant{reason: "NoPromptSources", status: metav1.ConditionFalse},
		},
		{
			name:    "removes the aggregate once every source is gone",
			agent:   agent,
			set:     promptSet{},
			objects: []client.Object{owned},
			want:    aggregateWant{reason: "NoPromptSources", status: metav1.ConditionFalse, writes: 1},
			check: func(t *testing.T, c *recordingClient) {
				t.Helper()
				if len(c.deletes) != 1 || c.deletes[0] != aggregate {
					t.Fatalf("deletes = %v", c.deletes)
				}
			},
		},
		{
			name:    "keeps a manual edit when every source is gone",
			agent:   agent,
			set:     promptSet{},
			objects: []client.Object{edited},
			want: aggregateWant{
				name:       aggregate,
				reason:     "NoPromptSources",
				status:     metav1.ConditionFalse,
				overridden: true,
				stale:      true,
			},
		},
		{
			name:    "keeps the last good aggregate while an aggregated agent is unavailable",
			agent:   agent,
			set:     promptSet{sourceUnavailable: true},
			objects: []client.Object{owned},
			want: aggregateWant{
				name:    aggregate,
				reason:  "SourceUnavailable",
				status:  metav1.ConditionFalse,
				stale:   true,
				requeue: 30 * time.Second,
			},
			check: func(t *testing.T, c *recordingClient) {
				t.Helper()
				if got := c.prompt(aggregate).Annotations[agentv1alpha1.AnnotationAggregatedInputHash]; got != "sha256:old" {
					t.Fatalf("input hash = %q, want the previous one", got)
				}
			},
		},
		{
			name:    "keeps the last good aggregate when only some sources resolve",
			agent:   agent,
			set:     partial,
			objects: []client.Object{owned},
			want: aggregateWant{
				name:    aggregate,
				reason:  "SourceUnavailable",
				status:  metav1.ConditionFalse,
				stale:   true,
				requeue: 30 * time.Second,
			},
		},
		{
			name:    "reports a name conflict instead of adopting a foreign prompt",
			agent:   agent,
			set:     testPromptSet("You are helpful."),
			objects: []client.Object{foreign},
			want:    aggregateWant{reason: "PromptNameConflict", status: metav1.ConditionFalse},
		},
		{
			name:  "does not create an aggregate from an empty source",
			agent: agent,
			set:   empty,
			want:  aggregateWant{reason: "SourceUnavailable", status: metav1.ConditionFalse, requeue: 30 * time.Second},
		},
		{
			name:    "reports every source as too many",
			agent:   agent,
			set:     promptSet{tooMany: true},
			objects: []client.Object{owned},
			want: aggregateWant{
				name:    aggregate,
				reason:  "TooManySources",
				status:  metav1.ConditionFalse,
				stale:   true,
				requeue: 30 * time.Second,
			},
		},
		{
			name:  "creates a degraded aggregate when no model is configured",
			agent: agent,
			set:   testPromptSet("First.", "Second."),
			want: aggregateWant{
				name:   aggregate,
				reason: "ModelMissing",
				status: metav1.ConditionFalse,
				stale:  true,
				// The fallback is deterministic but not what was asked for, so
				// the controller keeps retrying.
				requeue: 30 * time.Second,
				writes:  1,
			},
			check: func(t *testing.T, c *recordingClient) {
				t.Helper()
				stored := c.prompt(aggregate)
				if stored == nil || !strings.Contains(stored.Spec.Template, "First.") {
					t.Fatalf("stored prompt = %+v", stored)
				}
				// Without the input hash the next reconcile cannot mistake this
				// fallback for a result that is up to date.
				if _, ok := stored.Annotations[agentv1alpha1.AnnotationAggregatedInputHash]; ok {
					t.Fatalf("a degraded aggregate recorded its input hash")
				}
			},
		},
		{
			name:  "does nothing for an agent being deleted",
			agent: dying,
			set:   testPromptSet("You are helpful."),
			want:  aggregateWant{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, recorder := newAggregateRecorder(t, tc.objects...)

			got, err := rec.reconcileAggregatedPrompt(context.Background(), tc.agent, tc.set)
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if got.Name != tc.want.name {
				t.Errorf("name = %q, want %q", got.Name, tc.want.name)
			}
			if got.Condition.Reason != tc.want.reason {
				t.Errorf("reason = %q, want %q", got.Condition.Reason, tc.want.reason)
			}
			if got.Condition.Status != tc.want.status {
				t.Errorf("condition status = %q, want %q", got.Condition.Status, tc.want.status)
			}
			if got.Overridden != tc.want.overridden {
				t.Errorf("overridden = %v, want %v", got.Overridden, tc.want.overridden)
			}
			if got.Stale != tc.want.stale {
				t.Errorf("stale = %v, want %v", got.Stale, tc.want.stale)
			}
			if got.RequeueAfter != tc.want.requeue {
				t.Errorf("requeue = %s, want %s", got.RequeueAfter, tc.want.requeue)
			}
			if recorder.writes() != tc.want.writes {
				t.Errorf("writes = %d (create %d, update %d, delete %v), want %d",
					recorder.writes(), recorder.creates, recorder.updates, recorder.deletes, tc.want.writes)
			}
			if tc.check != nil {
				tc.check(t, recorder)
			}
		})
	}
}

// A second reconcile over unchanged inputs must write nothing. If the input
// hash written by upsertAggregatedPrompt stops matching the one recomputed from
// the same sources, every resync merges again — constant model spend for an
// identical result — and no test would notice.
func TestReconcileAggregatedPromptIsIdempotent(t *testing.T) {
	rec, recorder := newAggregateRecorder(t)
	agent := testAgent("a")
	set := testPromptSet("You are helpful.")

	first, err := rec.reconcileAggregatedPrompt(context.Background(), agent, set)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if recorder.creates != 1 {
		t.Fatalf("creates = %d, want 1", recorder.creates)
	}

	second, err := rec.reconcileAggregatedPrompt(context.Background(), agent, set)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if recorder.writes() != 1 {
		t.Fatalf("a second reconcile wrote again: create %d, update %d, delete %v",
			recorder.creates, recorder.updates, recorder.deletes)
	}
	if second.ContentHash != first.ContentHash || second.InputHash != first.InputHash {
		t.Fatalf("hashes drifted: %+v then %+v", first, second)
	}
	if second.Condition.Status != metav1.ConditionTrue || second.Stale {
		t.Fatalf("condition = %+v, stale = %v", second.Condition, second.Stale)
	}
}

func TestReconcileAggregatedPromptKeepsAManualEdit(t *testing.T) {
	rec, recorder := newAggregateRecorder(t)
	agent := testAgent("a")

	if _, err := rec.reconcileAggregatedPrompt(context.Background(), agent, testPromptSet("You are helpful.")); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	stored := recorder.prompt(agentv1alpha1.AggregatedPromptName(agent.Name))
	if stored == nil {
		t.Fatalf("no aggregate was created")
	}
	stored.Spec.Template = "edited by hand"

	got, err := rec.reconcileAggregatedPrompt(context.Background(), agent, testPromptSet("You are helpful."))
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if recorder.writes() != 1 {
		t.Fatalf("a manual edit was overwritten")
	}
	if !got.Overridden || got.Stale {
		t.Fatalf("overridden = %v, stale = %v; neither the content nor the sources changed", got.Overridden, got.Stale)
	}
	if got.Condition.Status != metav1.ConditionTrue || got.Condition.Reason != "Overridden" {
		t.Fatalf("condition = %+v", got.Condition)
	}

	// The same edit, with a source that moved underneath it: still not
	// overwritten, but now the user is told their copy is behind.
	got, err = rec.reconcileAggregatedPrompt(context.Background(), agent, testPromptSet("Changed."))
	if err != nil {
		t.Fatalf("third reconcile: %v", err)
	}
	if recorder.writes() != 1 {
		t.Fatalf("a manual edit was overwritten after a source changed")
	}
	if !got.Overridden || !got.Stale || got.Condition.Reason != "OverriddenStale" {
		t.Fatalf("overridden = %v, stale = %v, reason = %q", got.Overridden, got.Stale, got.Condition.Reason)
	}
}

func TestReconcileAggregatedPromptRegeneratesWhenASourceChanges(t *testing.T) {
	rec, recorder := newAggregateRecorder(t)
	agent := testAgent("a")

	first, err := rec.reconcileAggregatedPrompt(context.Background(), agent, testPromptSet("One."))
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	second, err := rec.reconcileAggregatedPrompt(context.Background(), agent, testPromptSet("Two."))
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	if recorder.updates != 1 || recorder.creates != 1 {
		t.Fatalf("create %d, update %d; want one of each", recorder.creates, recorder.updates)
	}
	stored := recorder.prompt(agentv1alpha1.AggregatedPromptName(agent.Name))
	if stored.Spec.Template != "Two." {
		t.Fatalf("stored template = %q", stored.Spec.Template)
	}
	if second.ContentHash == first.ContentHash {
		t.Fatalf("the content hash did not change with the content")
	}
	if !strings.HasPrefix(stored.Annotations[agentv1alpha1.AnnotationAggregatedInputHash], "sha256:") {
		t.Fatalf("input hash = %q", stored.Annotations[agentv1alpha1.AnnotationAggregatedInputHash])
	}
}

// A merge that fails must not replace content that worked. The failure is
// reachable in production (endpoint down, over budget); the previous prompt
// stays and the condition says why.
func TestReconcileAggregatedPromptKeepsGoodContentWhenTheMergeFails(t *testing.T) {
	var failing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			// 400 is not transient, so llmhttp returns on the first attempt.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"MERGED"}}]}`)
	}))
	defer server.Close()

	model := &agentv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "aggregator", Namespace: "default"},
		Spec: agentv1alpha1.ModelSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true},
			Endpoint:   server.URL,
			Model:      "test-model",
		},
	}
	rec, recorder := newAggregateRecorder(t, model)
	agent := testAgent("a")
	agent.Spec.ModelRef = &agentv1alpha1.Reference{Name: "aggregator"}

	first, err := rec.reconcileAggregatedPrompt(context.Background(), agent, testPromptSet("First.", "Second."))
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if recorder.creates != 1 || first.Stale {
		t.Fatalf("setup did not produce a good aggregate: %+v", first)
	}

	failing.Store(true)
	got, err := rec.reconcileAggregatedPrompt(context.Background(), agent, testPromptSet("First.", "Second, changed."))
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if recorder.writes() != 1 {
		t.Fatalf("a failed merge replaced the good content")
	}
	if stored := recorder.prompt(agentv1alpha1.AggregatedPromptName(agent.Name)); stored.Spec.Template != "MERGED" {
		t.Fatalf("stored template = %q", stored.Spec.Template)
	}
	if !got.Stale || got.Condition.Reason != "AggregationFailed" {
		t.Fatalf("stale = %v, condition = %+v", got.Stale, got.Condition)
	}
	if got.RequeueAfter != 30*time.Second {
		t.Fatalf("requeue = %s", got.RequeueAfter)
	}
}

// The regenerate token is the only way to force a rewrite when the inputs look
// unchanged, so it may only be recorded on a reconcile that actually rewrote
// something. Observing it on a failed attempt would consume the request.
func TestReconcileAggregatedPromptObservesTheTokenOnlyWhenItActs(t *testing.T) {
	agent := testAgent("a")
	agent.Spec.PromptAggregation = &agentv1alpha1.PromptAggregationSpec{RegenerateToken: "tok"}

	stuck := promptSet{
		declared:  []promptRef{{Namespace: "default", Name: "p1"}},
		available: nil,
	}
	rec, _ := newAggregateRecorder(t, aggregateOf(agent, "generated", "sha256:old"))
	got, err := rec.reconcileAggregatedPrompt(context.Background(), agent, stuck)
	if err != nil {
		t.Fatalf("unavailable reconcile: %v", err)
	}
	if got.ObservedToken != "" {
		t.Fatalf("observed token = %q; the request was consumed without being honoured", got.ObservedToken)
	}

	rec, _ = newAggregateRecorder(t)
	got, err = rec.reconcileAggregatedPrompt(context.Background(), agent, testPromptSet("You are helpful."))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got.ObservedToken != "tok" {
		t.Fatalf("observed token = %q, want the token that was acted on", got.ObservedToken)
	}
}

// TestCleanupInlinePromptReclaimsMigratedObjects covers the object shapes the
// cleanup must recognize now that the management API no longer adopts migrated
// inline Prompts: the controller's own object, and the unowned annotated object
// pg2crd leaves behind. Everything else is someone's and is left alone.
func TestCleanupInlinePromptReclaimsMigratedObjects(t *testing.T) {
	agent := testAgent("a")
	inlineName := agentv1alpha1.InlinePromptName(agent.Name)
	controller := true

	owned := &agentv1alpha1.Prompt{ObjectMeta: metav1.ObjectMeta{
		Name: inlineName, Namespace: "default",
		OwnerReferences: []metav1.OwnerReference{{
			Kind: "Agent", Name: agent.Name, Controller: &controller,
		}},
	}}
	migrated := &agentv1alpha1.Prompt{ObjectMeta: metav1.ObjectMeta{
		Name: inlineName, Namespace: "default",
		Annotations: map[string]string{agentv1alpha1.AnnotationInlineFor: agent.Name},
	}}
	foreign := &agentv1alpha1.Prompt{ObjectMeta: metav1.ObjectMeta{
		Name: inlineName, Namespace: "default",
		Annotations: map[string]string{agentv1alpha1.AnnotationInlineFor: "someone-else"},
	}}
	unannotated := &agentv1alpha1.Prompt{ObjectMeta: metav1.ObjectMeta{
		Name: inlineName, Namespace: "default",
	}}

	tests := []struct {
		name        string
		prompt      *agentv1alpha1.Prompt
		set         promptSet
		wantDeleted bool
	}{
		{"owned and unbound", owned, promptSet{}, true},
		{"migrated and unbound", migrated, promptSet{}, true},
		{"foreign annotation", foreign, promptSet{}, false},
		{"unannotated", unannotated, promptSet{}, false},
		{
			name:   "still bound",
			prompt: migrated,
			set: promptSet{
				declared: []promptRef{{Namespace: "default", Name: inlineName}},
			},
			wantDeleted: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, recorder := newAggregateRecorder(t, tc.prompt.DeepCopy())
			if err := r.cleanupInlinePrompt(context.Background(), agent, tc.set); err != nil {
				t.Fatalf("cleanupInlinePrompt() error = %v", err)
			}
			deleted := false
			for _, name := range recorder.deletes {
				if name == inlineName {
					deleted = true
				}
			}
			if deleted != tc.wantDeleted {
				t.Fatalf("deleted = %t, want %t (deletes: %v)", deleted, tc.wantDeleted, recorder.deletes)
			}
		})
	}
}
