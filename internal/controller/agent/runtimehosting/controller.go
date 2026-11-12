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

// Package runtimehosting runs the runtime instances a RuntimeHosting declares.
// Each instance serves one Agent; the session-to-instance pin lives in the
// gateway's RunStore, so this controller only has to keep the pool alive.
package runtimehosting

import (
	"context"
	"fmt"
	"hash/crc32"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/onexstack/onexai/internal/controller/support"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// ControllerName is the canonical name of this controller.
const ControllerName = "runtimehosting-controller"

// portBase is where instance ports start. The name hash spreads hostings over
// a range so two pools do not collide on their first instance.
const portBase = 18080

// Reconciler keeps a hosting pool aligned with the Agents that use its profile.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	mu        sync.Mutex
	processes map[string]*exec.Cmd
	ports     map[string]int
}

// Add wires the reconciler into the manager.
func Add(ctx context.Context, mgr ctrl.Manager, options controller.Options) (bool, error) {
	r := &Reconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorderFor(ControllerName),
		processes: make(map[string]*exec.Cmd),
		ports:     make(map[string]int),
	}
	if err := r.SetupWithManager(ctx, mgr, options); err != nil {
		return false, err
	}
	return true, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.RuntimeHosting{}).
		Watches(&agentv1alpha1.Agent{}, handler.EnqueueRequestsFromMapFunc(r.hostingsForAgent)).
		Named(ControllerName).
		WithOptions(options).
		Complete(r)
}

// finalizer keeps a deleted hosting from leaking its containers/processes.
const finalizer = "onexai.io/runtime-hosting"

// Reconcile converges the pool.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	hosting := &agentv1alpha1.RuntimeHosting{}
	if err := r.Get(ctx, req.NamespacedName, hosting); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !hosting.DeletionTimestamp.IsZero() {
		r.stopAll(hosting)
		if controllerutil.ContainsFinalizer(hosting, finalizer) {
			controllerutil.RemoveFinalizer(hosting, finalizer)
			if err := r.Update(ctx, hosting); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(hosting, finalizer) {
		controllerutil.AddFinalizer(hosting, finalizer)
		if err := r.Update(ctx, hosting); err != nil {
			return ctrl.Result{}, err
		}
	}

	before := hosting.DeepCopy()
	hosting.Status.ObservedGeneration = hosting.Generation

	instances, ready, reason, message := r.reconcileInstances(ctx, hosting)
	hosting.Status.Instances = instances
	support.SetReady(r.Recorder, hosting, &hosting.Status.Conditions, hosting.Generation, ready, reason, message)

	if !apiequality.Semantic.DeepEqual(&before.Status, &hosting.Status) {
		if err := r.Status().Patch(ctx, hosting, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, err
		}
	}
	// A pool is a live thing: health and idle state change without an object
	// edit, so reconcile again rather than waiting for a watch event.
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *Reconciler) reconcileInstances(
	ctx context.Context, hosting *agentv1alpha1.RuntimeHosting,
) ([]agentv1alpha1.RuntimeInstanceStatus, bool, string, string) {
	if !hosting.Spec.Enabled {
		r.stopAll(hosting)
		return nil, true, "Disabled", "hosting is disabled"
	}
	profile := &agentv1alpha1.RuntimeProfile{}
	key := types.NamespacedName{Namespace: hosting.Namespace, Name: hosting.Spec.ProfileRef.Name}
	if err := r.Get(ctx, key, profile); err != nil {
		r.stopAll(hosting)
		return nil, false, "ProfileNotFound", fmt.Sprintf("profile %q not found", key.Name)
	}

	agents := &agentv1alpha1.AgentList{}
	if err := r.List(ctx, agents, client.InNamespace(hosting.Namespace)); err != nil {
		return nil, false, "AgentListFailed", err.Error()
	}
	var desired []string
	for i := range agents.Items {
		agent := &agents.Items[i]
		if !agent.Spec.Enabled || agent.Spec.Runtime == nil || agent.Spec.Runtime.ProfileRef == nil {
			continue
		}
		if agent.Spec.Runtime.ProfileRef.Name != profile.Name {
			continue
		}
		desired = append(desired, agent.Name)
	}
	max := int(hosting.Spec.MaxInstances)
	if max <= 0 {
		max = 4
	}
	if len(desired) > max {
		desired = desired[:max]
	}
	if len(desired) == 0 {
		r.stopAll(hosting)
		return nil, true, "NoAgents", "no agent selects this profile"
	}

	statuses := make([]agentv1alpha1.RuntimeInstanceStatus, 0, len(desired))
	readyCount := 0
	for index, agentName := range desired {
		instanceID := instanceName(hosting.Name, agentName)
		port := r.portFor(instanceID, hosting.Name, index)
		endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
		instance := agentv1alpha1.RuntimeInstanceStatus{
			ID:       instanceID,
			Agent:    agentName,
			Endpoint: endpoint,
			State:    "starting",
		}
		if err := r.ensure(ctx, hosting, agentName, instanceID, port); err != nil {
			slog.WarnContext(ctx, "runtime hosting start failed", "hosting", hosting.Name, "agent", agentName, "error", err)
			instance.State = "failed"
		} else if healthy(endpoint) {
			instance.State = "ready"
			readyCount++
		}
		statuses = append(statuses, instance)
	}
	r.stopRemoved(hosting, statuses)
	if readyCount == len(desired) {
		return statuses, true, "InstancesReady", fmt.Sprintf("%d instance(s) ready", readyCount)
	}
	return statuses, false, "InstancesStarting", fmt.Sprintf("%d of %d instance(s) ready", readyCount, len(desired))
}

func (r *Reconciler) ensure(
	ctx context.Context, hosting *agentv1alpha1.RuntimeHosting, agentName, instanceID string, port int,
) error {
	switch hosting.Spec.Mode {
	case agentv1alpha1.HostingModeProcess:
		return r.ensureProcess(hosting, agentName, instanceID, port)
	case agentv1alpha1.HostingModeDocker:
		return r.ensureDocker(hosting, agentName, instanceID, port)
	default:
		return fmt.Errorf("unsupported hosting mode %q", hosting.Spec.Mode)
	}
}

func (r *Reconciler) ensureProcess(
	hosting *agentv1alpha1.RuntimeHosting, agentName, instanceID string, port int,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cmd := r.processes[instanceID]; cmd != nil && cmd.Process != nil && healthy(fmt.Sprintf("http://127.0.0.1:%d", port)) {
		return nil
	}
	if len(hosting.Spec.Command) == 0 {
		return fmt.Errorf("process hosting needs a command")
	}
	cmd := exec.Command(hosting.Spec.Command[0], hosting.Spec.Command[1:]...) //nolint:gosec // operator-configured
	cmd.Env = append(os.Environ(), runtimeEnv(hosting, agentName, port)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.processes[instanceID] = cmd
	go func() { _ = cmd.Wait() }()
	return nil
}

func (r *Reconciler) ensureDocker(
	hosting *agentv1alpha1.RuntimeHosting, agentName, instanceID string, port int,
) error {
	if healthy(fmt.Sprintf("http://127.0.0.1:%d", port)) {
		return nil
	}
	_ = exec.Command("docker", "rm", "-f", instanceID).Run() //nolint:gosec // best-effort replacement
	args := []string{
		"run", "-d", "--rm", "--name", instanceID,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, port),
	}
	for _, env := range runtimeEnv(hosting, agentName, port) {
		args = append(args, "-e", env)
	}
	args = append(args, hosting.Spec.Image)
	if len(hosting.Spec.Command) > 0 {
		args = append(args, hosting.Spec.Command...)
	}
	output, err := exec.Command("docker", args...).CombinedOutput() //nolint:gosec // operator-configured
	if err != nil {
		return fmt.Errorf("docker run: %w: %s", err, string(output))
	}
	return nil
}

func runtimeEnv(hosting *agentv1alpha1.RuntimeHosting, agentName string, port int) []string {
	env := []string{
		"ONEXAI_RUNTIME_PROFILE=" + hosting.Spec.ProfileRef.Name,
		"ONEXAI_AGENT_NAME=" + agentName,
		"ONEXAI_RUNTIME_PORT=" + strconv.Itoa(port),
	}
	// A container cannot dial the host's loopback; host.docker.internal is the
	// address Docker Desktop exposes for exactly this.
	if hosting.Spec.Mode == agentv1alpha1.HostingModeDocker {
		env = append(env, "ONEXAI_CAPABILITY_URL=http://host.docker.internal:8091/v1/mcp")
	} else {
		env = append(env, "ONEXAI_CAPABILITY_URL=http://127.0.0.1:8091/v1/mcp")
	}
	for _, item := range hosting.Spec.Env {
		if item.Name == "" {
			continue
		}
		if item.ValueFrom != nil {
			// Secret-backed env is intentionally unsupported here: the
			// controller would have to copy a credential into a process it does
			// not own. Use EgressPolicy or an external endpoint instead.
			continue
		}
		env = append(env, item.Name+"="+item.Value)
	}
	return env
}

func (r *Reconciler) portFor(instanceID, hostingName string, index int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if port, ok := r.ports[instanceID]; ok {
		return port
	}
	base := portBase + int(crc32.ChecksumIEEE([]byte(hostingName))%100)*10 + index
	for port := base; port < base+100; port++ {
		if listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			_ = listener.Close()
			r.ports[instanceID] = port
			return port
		}
	}
	r.ports[instanceID] = base
	return base
}

func (r *Reconciler) stopRemoved(hosting *agentv1alpha1.RuntimeHosting, keep []agentv1alpha1.RuntimeInstanceStatus) {
	kept := make(map[string]struct{}, len(keep))
	for _, instance := range keep {
		kept[instance.ID] = struct{}{}
	}
	for _, instance := range hosting.Status.Instances {
		if _, ok := kept[instance.ID]; ok {
			continue
		}
		r.stop(instance.ID)
	}
}

func (r *Reconciler) stopAll(hosting *agentv1alpha1.RuntimeHosting) {
	for _, instance := range hosting.Status.Instances {
		r.stop(instance.ID)
	}
}

func (r *Reconciler) stop(instanceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cmd := r.processes[instanceID]; cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		delete(r.processes, instanceID)
		return
	}
	_ = exec.Command("docker", "rm", "-f", instanceID).Run() //nolint:gosec // best-effort stop
}

func healthy(endpoint string) bool {
	hostPort := endpoint[len("http://"):]
	conn, err := net.DialTimeout("tcp", hostPort, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func instanceName(hosting, agent string) string {
	return fmt.Sprintf("onexai-rt-%s-%s", hosting, agent)
}

func (r *Reconciler) hostingsForAgent(ctx context.Context, obj client.Object) []reconcile.Request {
	agent, ok := obj.(*agentv1alpha1.Agent)
	if !ok || agent.Spec.Runtime == nil || agent.Spec.Runtime.ProfileRef == nil {
		return nil
	}
	hostings := &agentv1alpha1.RuntimeHostingList{}
	if err := r.List(ctx, hostings, client.InNamespace(agent.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range hostings.Items {
		hosting := &hostings.Items[i]
		if hosting.Spec.ProfileRef.Name != agent.Spec.Runtime.ProfileRef.Name {
			continue
		}
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: hosting.Namespace, Name: hosting.Name,
		}})
	}
	return requests
}
